//go:build linux

package exportproxy

import "testing"

func TestValidInterfaceName(t *testing.T) {
	for _, value := range []string{"wwan0", "wwp0s20f0u5i4", "rmnet_data0", "usb.1"} {
		if !validInterfaceName(value) {
			t.Errorf("validInterfaceName(%q) = false", value)
		}
	}
	for _, value := range []string{"", ".", "..", "../wwan0", `..\wwan0`, "wwan0/evil", "interface-name-too-long"} {
		if validInterfaceName(value) {
			t.Errorf("validInterfaceName(%q) = true", value)
		}
	}
}
