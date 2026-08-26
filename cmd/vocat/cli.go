package main

import (
	"fmt"
	"io"

	"vocat/internal/buildinfo"
)

func runVersion() {
	fmt.Println("vocat " + buildinfo.Build())
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `vocat %s

Usage:
  vocat              No arguments: interactive management menu when run as
                     root on a TTY, otherwise the server. A managed service
                     (systemd or OpenWrt procd) starts the server unchanged.
  vocat serve        Run the server in the foreground (use from a TTY when
                     vocat without arguments would enter the menu).
  vocat version      Print the build version and exit.
  vocat doctor       Diagnose USB modem, AT, QMI, PC/SC and proxy UDP paths.
                     Use --repair-dji-qmi on Linux to restore the factory-ID
                     DJI/Baiwang 2ca3:4006 AT/QMI interface bindings and wake
                     QMI without changing NV.
  vocat carrier import-ipcc [flags] FILE.ipcc
                     Convert an Apple carrier bundle into a reviewable VoCat
                     profile. Preview is the default; --install writes it to
                     carrier-profiles.d and takes effect after restart.
                     Flags: --bundle NAME --id ID --document-only --install
                            --profile-dir DIR.
  vocat update       Check GitHub for a newer release and self-update.
                     Flags:
                       --check           Only report whether an update is available.
                       --repo owner/name GitHub repository (default: $VOCAT_REPO or MengMengCode/VoCat).
                       --target path     Binary to replace (default: running exe).
                       --force           Reinstall even at the same version.
                     Environment:
                       VOCAT_REPO        Fallback for --repo.
                       VOCAT_IMS_USER_AGENT
                                         Fallback User-Agent when the carrier
                                         profile does not define one.
                       GITHUB_TOKEN      Optional bearer token for private repos
                                         or higher rate limits.
  vocat menu         Interactive lifecycle menu (root on the host):
                       toggle language, change password, change the Web port,
                       restart, update, uninstall.
  vocat help         Show this help message.

When run without a subcommand on a non-TTY (e.g. systemd), vocat starts the
HTTP server using VOCAT_* environment variables or $VOCAT_CONFIG for
configuration.
`, buildinfo.Version)
}
