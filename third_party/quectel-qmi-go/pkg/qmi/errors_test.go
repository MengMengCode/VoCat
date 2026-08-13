package qmi

import (
	"fmt"
	"testing"
)

func TestGetQMIErrorUnwrapsWrappedError(t *testing.T) {
	root := &QMIError{
		Service:   ServiceDMS,
		MessageID: DMSSetOperatingMode,
		Result:    0x0001,
		ErrorCode: QMIErrDeviceNotReady,
	}
	err := fmt.Errorf("set operating mode failed: %w", root)

	got := GetQMIError(err)
	if got != root {
		t.Fatalf("GetQMIError()=%v, want wrapped root QMIError", got)
	}
	if !IsQMIError(err) {
		t.Fatal("IsQMIError()=false, want true for wrapped QMIError")
	}
}
