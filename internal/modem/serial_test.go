package modem

import "testing"

func TestIsNativeWWANATPath(t *testing.T) {
	for _, path := range []string{
		"/dev/wwan0at0",
		"/dev/wwan12at3",
	} {
		if !isNativeWWANATPath(path) {
			t.Errorf("isNativeWWANATPath(%q) = false", path)
		}
	}
	for _, path := range []string{
		"/dev/wwan0qmi0",
		"/dev/ttyUSB2",
		"/tmp/wwan-at",
		"/dev/wwanat0",
	} {
		if isNativeWWANATPath(path) {
			t.Errorf("isNativeWWANATPath(%q) = true", path)
		}
	}
}
