package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"vocat/internal/modem"
	"vocat/internal/pcsc"
	"vocat/internal/proxy"
)

type doctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Evidence any    `json:"evidence,omitempty"`
}

type doctorReport struct {
	Time   time.Time     `json:"time"`
	OS     string        `json:"os"`
	Arch   string        `json:"arch"`
	Checks []doctorCheck `json:"checks"`
}

type djiQMIRepairResult struct {
	USBName          string `json:"usb_name"`
	Interface        string `json:"interface"`
	USBDevice        string `json:"usb_device"`
	OriginalDriver   string `json:"original_driver,omitempty"`
	ControlDevice    string `json:"control_device"`
	NetworkInterface string `json:"network_interface,omitempty"`
	QMIProbe         string `json:"qmi_probe"`
	Attempts         int    `json:"attempts"`
}

func runDoctor(args []string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	proxyAddress := flags.String("proxy", "", "SOCKS5 host:port to test")
	proxyUsername := flags.String("proxy-username", "", "SOCKS5 username")
	passwordEnv := flags.String("proxy-password-env", "VOCAT_DOCTOR_PROXY_PASSWORD", "environment variable containing the proxy password")
	repairDJI := flags.Bool("repair-dji-qmi", false, "rebind DJI 2ca3:4006 interface 4 to qmi_wwan and assert DTR (Linux/root only; no NV write)")
	jsonOutput := flags.Bool("json", false, "write machine-readable JSON")
	timeout := flags.Duration("timeout", 12*time.Second, "per-probe timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 || *timeout > time.Minute {
		return errors.New("usage: vocat doctor [--repair-dji-qmi] [--proxy host:port] [--proxy-username name] [--proxy-password-env ENV] [--json]")
	}
	report := doctorReport{Time: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	add := func(name, status, code, message string, evidence any) {
		report.Checks = append(report.Checks, doctorCheck{Name: name, Status: status, Code: code, Message: message, Evidence: evidence})
	}

	if data, err := os.ReadFile("/proc/version"); err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft") {
		add("host", "warning", "wsl_usbip_detected", "WSL/USBIP detected; QMI control transfers may time out even when /dev/cdc-wdm exists", nil)
	} else {
		add("host", "passed", "native_host", "No WSL kernel marker detected", nil)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *repairDJI {
		result, err := repairDJIQMI(ctx)
		if err != nil {
			return fmt.Errorf("repair DJI QMI binding: %w", err)
		}
		add("dji_qmi_repair", "passed", "dji_qmi_dtr_asserted", "DJI interface 4 was bound to qmi_wwan after a transient CDC DTR assertion; modem NV and USB identity were not changed", result)
	}

	candidates, discoverErr := modem.NewSystemDiscoverer().Discover(ctx)
	if discoverErr != nil {
		add("modem_discovery", "failed", "modem_discovery_failed", discoverErr.Error(), nil)
	} else if len(candidates) == 0 {
		add("modem_discovery", "warning", "no_modem", "No USB modem was discovered", nil)
	} else {
		add("modem_discovery", "passed", "modem_discovered", fmt.Sprintf("Discovered %d modem candidate(s)", len(candidates)), candidates)
	}
	for _, candidate := range candidates {
		name := "modem:" + candidate.ID
		if candidate.HasATPort() {
			probeContext, cancelProbe := context.WithTimeout(context.Background(), minDuration(*timeout, 5*time.Second))
			client, openErr := (modem.SerialOpener{}).Open(probeContext, candidate.ATPort)
			if openErr != nil {
				add(name+":at", "warning", "at_open_failed", openErr.Error(), candidate.ATPort.OpenPath())
			} else {
				response, commandErr := client.Execute(probeContext, "AT+CFUN?")
				_ = client.Close()
				if commandErr != nil {
					add(name+":at", "warning", "at_probe_failed", commandErr.Error(), candidate.ATPort.OpenPath())
				} else {
					add(name+":at", "passed", "at_ready", "AT control channel responded to a read-only CFUN query", response.Text())
				}
			}
			cancelProbe()
		} else {
			add(name+":at", "failed", "at_missing", "No AT port was selected", nil)
		}
		if strings.TrimSpace(candidate.QMIControl) == "" {
			add(name+":qmi", "warning", "qmi_missing", "No cdc-wdm/QMI control node was discovered", nil)
		} else if qmicli, lookErr := exec.LookPath("qmicli"); lookErr != nil {
			add(name+":qmi", "warning", "qmicli_missing", "QMI node exists but qmicli is unavailable for an active DMS check", candidate.QMIControl)
		} else {
			probeContext, cancelProbe := context.WithTimeout(context.Background(), minDuration(*timeout, 8*time.Second))
			command := exec.CommandContext(probeContext, qmicli, "-d", candidate.QMIControl, "--dms-get-operating-mode")
			output, commandErr := command.CombinedOutput()
			message := strings.TrimSpace(string(output))
			cancelProbe()
			if commandErr != nil {
				code := "qmi_cid_failed"
				if errors.Is(probeContext.Err(), context.DeadlineExceeded) || strings.Contains(strings.ToLower(message), "timed out") {
					code = "qmi_cid_timeout"
				}
				add(name+":qmi", "failed", code, "qmicli DMS client allocation/read failed", message)
			} else {
				add(name+":qmi", "passed", "qmi_dms_ready", "qmicli allocated DMS and completed a read-only request", message)
			}
		}
	}

	readers, readerErr := pcsc.New().Readers(ctx)
	if readerErr == nil {
		add("pcsc", "passed", "pcsc_ready", fmt.Sprintf("PC/SC reported %d reader(s)", len(readers)), readers)
	} else if errors.Is(readerErr, pcsc.ErrUnsupported) || errors.Is(readerErr, pcsc.ErrUnavailable) {
		add("pcsc", "warning", "pcsc_unavailable", readerErr.Error(), nil)
	} else {
		add("pcsc", "failed", "pcsc_failed", readerErr.Error(), nil)
	}

	if strings.TrimSpace(*proxyAddress) != "" {
		password := os.Getenv(strings.TrimSpace(*passwordEnv))
		probeContext, cancelProbe := context.WithTimeout(context.Background(), *timeout)
		result, probeErr := proxy.ProbeSOCKS5(probeContext, *proxyAddress, *proxyUsername, password, *timeout)
		cancelProbe()
		status := "passed"
		if probeErr != nil {
			status = "failed"
		}
		add("proxy_udp", status, result.Diagnosis, result.Hint, result)
	}

	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	for _, check := range report.Checks {
		fmt.Printf("%-8s %-26s %-28s %s\n", strings.ToUpper(check.Status), check.Name, check.Code, check.Message)
	}
	return nil
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
