//go:build linux

package device

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"vocat/internal/modem"
)

func readPlatformRegistration(ctx context.Context, candidate modem.Candidate) (platformRegistration, bool) {
	control := strings.TrimSpace(candidate.QMIControl)
	if control == "" {
		return platformRegistration{}, false
	}
	qmicli, err := exec.LookPath("qmicli")
	if err != nil {
		return platformRegistration{}, false
	}
	queryContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		queryContext,
		qmicli,
		"-d", control,
		"--device-open-proxy",
		"--nas-get-serving-system",
	).CombinedOutput()
	if err != nil {
		return platformRegistration{}, false
	}
	return parseQMIRegistration(string(output))
}
