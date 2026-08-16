package modem

import "testing"

func TestIsDJI4GUSBIdentity(t *testing.T) {
	tests := []struct {
		name      string
		vendorID  string
		productID string
		want      bool
	}{
		{name: "DJI 4G module", vendorID: "2ca3", productID: "4006", want: true},
		{name: "DJI 4G module uppercase", vendorID: "2CA3", productID: "4006", want: true},
		{name: "unrelated DJI device", vendorID: "2ca3", productID: "001f", want: false},
		{name: "Quectel identity", vendorID: "2c7c", productID: "0125", want: false},
		{name: "unrelated USB device", vendorID: "0403", productID: "6001", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsDJI4GUSB(test.vendorID, test.productID); got != test.want {
				t.Fatalf("IsDJI4GUSB(%q, %q) = %v, want %v", test.vendorID, test.productID, got, test.want)
			}
		})
	}
}

func TestSelectATPortPrefersTTYUSB2AcrossUSBCompositions(t *testing.T) {
	ports := []Port{
		{Name: "ttyUSB2", InterfaceNumber: 0x02, Role: PortRoleDiagnostic},
		{Name: "ttyUSB3", InterfaceNumber: 0x05, Role: PortRoleModem},
	}
	selected := selectATPort(ports)
	if selected.Name != "ttyUSB2" || selected.InterfaceNumber != 0x02 {
		t.Fatalf("selected %#v, want ttyUSB2 on interface 02", selected)
	}
}
