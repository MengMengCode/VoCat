// Package update implements the `vocat update` self-updater. It queries the
// GitHub Releases API for a newer build, downloads the matching Linux binary
// for the current architecture, verifies it against a published SHA256SUMS,
// atomically replaces the running binary on disk, and restarts the vocat
// systemd unit.
//
// Trust model: GitHub TLS guarantees the channel; the repository owner controls
// which assets are published; SHA256SUMS guards integrity. There is no GPG
// signature verification — an accepted trade-off for a closed-network testing
// tool. Both the CLI and authenticated web UI use this same verified replacement
// path.
package update

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"vocat/internal/buildinfo"
)

// Options captures the resolved flags for an update invocation.
type Options struct {
	Check  bool   // report-only
	Repo   string // owner/name
	Target string // binary path to replace
	Force  bool   // reinstall even at equal version
	Token  string // optional GitHub bearer token
	Help   bool   // print usage, do nothing
}

// Run executes the update subcommand. It returns nil on success or when an
// update is reported-but-not-applied under --check; it returns an error only
// when something concrete went wrong.
func Run(logger *slog.Logger, args []string) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}
	if opts.Help {
		printUpdateUsage()
		return nil
	}
	if opts.Repo == "" {
		opts.Repo = strings.TrimSpace(os.Getenv("VOCAT_REPO"))
	}
	if opts.Repo == "" {
		opts.Repo = DefaultRepository
	}
	if opts.Token == "" {
		opts.Token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if opts.Target == "" {
		opts.Target = resolveDefaultTarget()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	logger.Info("checking for updates", "repo", opts.Repo, "current", buildinfo.Version)
	result, err := CheckLatest(ctx, opts.Repo, opts.Token, buildinfo.Version)
	if err != nil {
		return err
	}
	if !result.Available && !opts.Force {
		logger.Info("already up to date", "version", buildinfo.Version)
		fmt.Printf("vocat %s is already the latest release.\n", buildinfo.Version)
		return nil
	}
	if opts.Check {
		fmt.Printf("update available: %s -> %s\n", buildinfo.Version, result.Latest)
		if result.ReleaseNotes != "" {
			fmt.Println(result.ReleaseNotes)
		}
		return nil
	}

	logger.Info("update available", "current", buildinfo.Version, "latest", result.Latest)
	return applyUpdate(ctx, logger, opts, result.Release, result.Latest, true)
}

// ApplyLatest downloads, verifies, and atomically installs the newest trusted
// release. HTTP callers can pass restart=false and restart after flushing the
// response.
func ApplyLatest(ctx context.Context, logger *slog.Logger, opts Options, restart bool) (CheckResult, error) {
	if strings.TrimSpace(opts.Repo) == "" {
		opts.Repo = DefaultRepository
	}
	if strings.TrimSpace(opts.Token) == "" {
		opts.Token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if strings.TrimSpace(opts.Target) == "" {
		opts.Target = resolveDefaultTarget()
	}
	result, err := CheckLatest(ctx, opts.Repo, opts.Token, buildinfo.Version)
	if err != nil {
		return CheckResult{}, err
	}
	if !result.Available && !opts.Force {
		return result, nil
	}
	if err := applyUpdate(ctx, logger, opts, result.Release, result.Latest, restart); err != nil {
		return CheckResult{}, err
	}
	result.Applied = true
	return result, nil
}

func applyUpdate(ctx context.Context, logger *slog.Logger, opts Options, release *Release, latest string, restart bool) error {
	assetNames := assetNamesFor(runtime.GOOS, runtime.GOARCH)
	var asset *Asset
	for _, name := range assetNames {
		if asset = findAsset(release, name); asset != nil {
			break
		}
	}
	if asset == nil {
		return fmt.Errorf("update: release %s has none of assets %q for %s/%s", release.TagName, assetNames, runtime.GOOS, runtime.GOARCH)
	}

	sumsAsset := findAsset(release, "SHA256SUMS")
	if sumsAsset == nil {
		return fmt.Errorf("update: release %s missing SHA256SUMS — refusing to install unverified", release.TagName)
	}

	// The temp file MUST live in the same directory as the target so os.Rename
	// stays on one filesystem; a cross-device rename fails with EXDEV.
	targetDir := filepath.Dir(opts.Target)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("update: ensure target dir %s: %w", targetDir, err)
	}
	tmp, err := os.CreateTemp(targetDir, ".vocat-update-*")
	if err != nil {
		return fmt.Errorf("update: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	defer func() {
		if tmp != nil {
			_ = tmp.Close()
		}
	}()

	logger.Info("downloading binary", "asset", asset.Name, "size", asset.Size, "url", asset.BrowserDownloadURL)
	if err := downloadAsset(ctx, asset.BrowserDownloadURL, opts.Token, tmp); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("update: finalize temp file: %w", err)
	}
	tmp = nil

	var sums bytes.Buffer
	if err := downloadAsset(ctx, sumsAsset.BrowserDownloadURL, opts.Token, &sums); err != nil {
		cleanup()
		return err
	}
	expectedHash, err := ParseSHA256SUMS(sums.String(), asset.Name)
	if err != nil {
		cleanup()
		return err
	}
	ok, err := VerifyFileSHA256(tmpPath, expectedHash)
	if err != nil {
		cleanup()
		return err
	}
	if !ok {
		cleanup()
		return fmt.Errorf("update: sha256 mismatch for %s — refusing to install", asset.Name)
	}
	logger.Info("verified binary", "sha256", expectedHash)

	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("update: chmod temp binary: %w", err)
	}
	if err := validateExecutable(ctx, tmpPath); err != nil {
		cleanup()
		return err
	}
	if err := backupAndReplace(opts.Target, tmpPath); err != nil {
		cleanup()
		return err
	}
	logger.Info("installed new binary", "target", opts.Target, "version", latest)
	fmt.Printf("vocat updated to %s.\n", latest)

	if restart {
		if err := RestartService(logger); err != nil {
			// The file replacement already succeeded; a restart failure is not
			// fatal — the operator can restart the service manually.
			fmt.Printf("Binary replaced, but automatic restart failed: %v\n", err)
			fmt.Println("Restart the vocat service manually to apply the new build.")
		}
	}
	return nil
}

// validateExecutable catches incompatible architectures and missing dynamic
// loaders before the working installation is touched. A valid checksum alone
// cannot detect those packaging errors.
func validateExecutable(ctx context.Context, path string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: downloaded binary cannot run on this host: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	if !strings.Contains(strings.ToLower(string(output)), "vocat") {
		return fmt.Errorf("update: downloaded binary returned an unexpected version response: %q", strings.TrimSpace(string(output)))
	}
	return nil
}

// backupAndReplace renames the current binary aside, then moves the verified
// temp file into place. Both renames are atomic on the same filesystem. The
// previous working binary is retained for service-level or manual rollback.
func backupAndReplace(target, tmp string) error {
	backup := target + ".previous"
	if _, err := os.Stat(target); err == nil {
		_ = os.Remove(backup)
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("update: move current binary aside: %w", err)
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		// Best-effort rollback so the operator is not left without a binary.
		if _, statErr := os.Stat(backup); statErr == nil {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("update: move new binary into place: %w", err)
	}
	return nil
}

// RestartService supports both systemd hosts and OpenWrt/procd routers.
func RestartService(logger *slog.Logger) error {
	if _, err := os.Stat("/etc/init.d/vocat"); err == nil {
		cmd := exec.Command("/etc/init.d/vocat", "restart")
		if out, err := cmd.CombinedOutput(); err != nil {
			logger.Warn("OpenWrt service restart failed", "error", err, "output", string(out))
			return fmt.Errorf("restart OpenWrt vocat service: %w", err)
		}
		return nil
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("neither /etc/init.d/vocat nor systemctl is available")
	}
	// Queue the restart and let systemctl exit before systemd stops this unit.
	// A blocking restart command becomes part of vocat.service's own cgroup and
	// waits for that same cgroup to terminate, creating a stop-timeout cycle.
	cmd := exec.Command("systemctl", "restart", "--no-block", "vocat")
	if out, err := cmd.CombinedOutput(); err != nil {
		logger.Warn("systemctl restart failed", "error", err, "output", string(out))
		return fmt.Errorf("systemctl restart vocat: %w", err)
	}
	return nil
}

// resolveDefaultTarget returns the conventional install path when present,
// falling back to the running executable. This lets `vocat update` "just work"
// on the standard systemd host without flags.
func resolveDefaultTarget() string {
	const defaultPath = "/opt/vocat/bin/vocat"
	if _, err := os.Stat(defaultPath); err == nil {
		return defaultPath
	}
	exe, err := os.Executable()
	if err != nil {
		return defaultPath
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe
	}
	return resolved
}

func findAsset(release *Release, name string) *Asset {
	for i := range release.Assets {
		if release.Assets[i].Name == name {
			return &release.Assets[i]
		}
	}
	return nil
}

func assetNamesFor(goos, goarch string) []string {
	if goos == "linux" && goarch == "arm64" {
		// AArch64 and arm64 name the same instruction set. Prefer the historic
		// release name and accept the explicit architecture alias as fallback.
		return []string{"vocat-linux-arm64", "vocat-linux-aarch64"}
	}
	if goos == "linux" && goarch == "arm" {
		// Official 32-bit ARM builds target GOARM=7. Keep the generic legacy
		// name as a fallback for installations consuming an older release.
		return []string{"vocat-linux-armv7", "vocat-linux-arm"}
	}
	return []string{fmt.Sprintf("vocat-%s-%s", goos, goarch)}
}

func printUpdateUsage() {
	fmt.Println(`Usage: vocat update [flags]

Fetch the latest release from GitHub and replace this binary in place.

Flags:
  --check            Report whether an update is available, then exit.
  --force            Reinstall even when already at the latest version.
  --repo owner/name  GitHub repository (default: $VOCAT_REPO or MengMengCode/VoCat).
  --target path      Binary to replace (default: /opt/vocat/bin/vocat if
                     present, otherwise the running executable).
  --token token      GitHub bearer token (default: $GITHUB_TOKEN).
  -h, --help         Show this help.

Environment:
  VOCAT_REPO         Fallback for --repo.
  GITHUB_TOKEN       Fallback for --token. Required for private repos and
                     recommended to avoid unauthenticated rate limits.`)
}

func parseFlags(args []string) (Options, error) {
	var opts Options
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--check":
			opts.Check = true
		case arg == "--force":
			opts.Force = true
		case arg == "--repo":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --repo requires a value")
			}
			opts.Repo = args[i]
		case strings.HasPrefix(arg, "--repo="):
			opts.Repo = strings.TrimPrefix(arg, "--repo=")
		case arg == "--target":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --target requires a value")
			}
			opts.Target = args[i]
		case strings.HasPrefix(arg, "--target="):
			opts.Target = strings.TrimPrefix(arg, "--target=")
		case arg == "--token":
			i++
			if i >= len(args) {
				return opts, fmt.Errorf("update: --token requires a value")
			}
			opts.Token = args[i]
		case strings.HasPrefix(arg, "--token="):
			opts.Token = strings.TrimPrefix(arg, "--token=")
		case arg == "-h" || arg == "--help":
			opts.Help = true
		default:
			return opts, fmt.Errorf("update: unknown flag %q", arg)
		}
	}
	return opts, nil
}
