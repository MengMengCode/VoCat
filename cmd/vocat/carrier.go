package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"vocat/internal/config"
	"vocat/internal/vowifi"
)

func runCarrier(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: vocat carrier import-ipcc [flags] FILE.ipcc")
	}
	switch args[0] {
	case "import-ipcc":
		return runCarrierImportIPCC(args[1:], stdout)
	default:
		return fmt.Errorf("unknown carrier subcommand %q", args[0])
	}
}

func runCarrierImportIPCC(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("carrier import-ipcc", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var bundle string
	var profileID string
	var profileDir string
	var install bool
	var documentOnly bool
	flags.StringVar(&bundle, "bundle", "", "bundle name when an IPCC contains more than one carrier bundle")
	flags.StringVar(&profileID, "id", "", "override the generated carrier profile ID")
	flags.StringVar(&profileDir, "profile-dir", "", "installation directory (default: next to the VoCat database)")
	flags.BoolVar(&install, "install", false, "atomically install the reviewed generated profile")
	flags.BoolVar(&documentOnly, "document-only", false, "print only the generated carrier profile document")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: vocat carrier import-ipcc [--bundle NAME] [--id ID] [--document-only] [--install] [--profile-dir DIR] FILE.ipcc")
	}
	if documentOnly && install {
		return errors.New("--document-only and --install cannot be used together")
	}
	if strings.TrimSpace(profileDir) != "" && !install {
		return errors.New("--profile-dir requires --install")
	}
	result, err := vowifi.ImportCarrierIPCC(flags.Arg(0), vowifi.IPCCImportOptions{
		Bundle:    bundle,
		ProfileID: profileID,
	})
	if err != nil {
		return err
	}
	if documentOnly {
		_, err := stdout.Write(result.Document)
		return err
	}

	installedPath := ""
	if install {
		profileDir = strings.TrimSpace(profileDir)
		if profileDir == "" {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load configuration for carrier profile directory: %w", err)
			}
			profileDir = filepath.Join(filepath.Dir(cfg.DatabasePath), "carrier-profiles.d")
		}
		installedPath, err = vowifi.InstallCarrierIPCCResult(result, profileDir)
		if err != nil {
			return err
		}
		if absolute, absoluteErr := filepath.Abs(installedPath); absoluteErr == nil {
			installedPath = absolute
		}
	}
	output := struct {
		vowifi.IPCCImportResult
		InstalledPath   string `json:"installed_path,omitempty"`
		RestartRequired bool   `json:"restart_required,omitempty"`
	}{
		IPCCImportResult: result,
		InstalledPath:    installedPath,
		RestartRequired:  installedPath != "",
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
