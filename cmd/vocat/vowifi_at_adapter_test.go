package main

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
	"vocat/internal/vowifi"
)

func TestVoWiFiAdapterResolvesPhysicalIdentityAndKeepsDedicatedTransports(t *testing.T) {
	ec20, zxic := &vowifi.EC20Adapter{}, &vowifi.ZXICAdapter{}
	pcsc, native := &vowifi.PCSCAdapter{}, &vowifi.NativeQMIAdapter{}
	for _, tt := range []struct {
		name, deviceType, vendor, product string
		want                              vowifiDeviceAdapter
	}{
		{"old PCIe UZ901", store.DeviceTypePCIeEC20EC25, "19d2", "0536", zxic},
		{"unverified sibling", store.DeviceTypePCIeEC20EC25, "19d2", "ffff", ec20},
		{"incorrect ZXIC type", store.DeviceTypeZXIC, "19d2", "ffff", ec20},
		{"Quectel", store.DeviceTypePCIeEC20EC25, "2c7c", "0125", ec20},
		{"PCSC priority", store.DeviceTypeUSBSIMReader, "19d2", "0536", pcsc},
		{"native QMI priority", store.DeviceTypeWiFi410, "19d2", "0536", native},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(context.Context) (device.Device, error) {
				if tt.want == pcsc || tt.want == native {
					t.Fatal("dedicated transports must not consult the AT resolver")
				}
				return device.Device{Discovered: true, Candidate: modem.Candidate{VendorID: tt.vendor, ProductID: tt.product}}, nil
			}
			got := voWiFiAdapterForDevice(tt.deviceType, lookup, ec20, zxic, pcsc, native)
			if resolved, ok := got.(*resolvedATVoWiFiAdapter); ok {
				got = resolved.current(context.Background())
			}
			if got != tt.want {
				t.Fatalf("adapter = %T, want %T", got, tt.want)
			}
		})
	}
}

func TestResolvedATVoWiFiAdapterRecoversAfterOfflineStartupAndUnplug(t *testing.T) {
	ec20, zxic := &vowifi.EC20Adapter{}, &vowifi.ZXICAdapter{}
	available := false
	adapter := newResolvedATVoWiFiAdapter(store.DeviceTypePCIeEC20EC25, func(context.Context) (device.Device, error) {
		if !available {
			return device.Device{}, device.ErrNotFound
		}
		return device.Device{Discovered: true, Candidate: modem.Candidate{VendorID: "19d2", ProductID: "0536"}}, nil
	}, ec20, zxic)
	if adapter.current(context.Background()) != ec20 {
		t.Fatal("offline startup should retain the configured fallback")
	}
	available = true
	if adapter.current(context.Background()) != zxic {
		t.Fatal("rediscovered UZ901 must use ZXIC without recreating the runtime")
	}
	available = false
	if adapter.current(context.Background()) != zxic {
		t.Fatal("unplug must retain the previous adapter's radio checkpoint")
	}
}

type vowifiInventoryExecutor struct{}

func (vowifiInventoryExecutor) ExecuteAT(_ context.Context, _ string, command string) (modem.Response, error) {
	switch command {
	case "AT+CFUN?":
		return modem.Response{Lines: []string{"+CFUN: 4"}, Final: "OK"}, nil
	case "AT+CGACT?":
		return modem.Response{}, &modem.CommandError{Command: command, Final: "+CME ERROR: 148"}
	default:
		return modem.Response{}, fmt.Errorf("unexpected command %q", command)
	}
}

func (vowifiInventoryExecutor) ExecuteAPDU(context.Context, string, modem.APDUCommand, int, []byte) ([]byte, error) {
	return nil, fmt.Errorf("unexpected APDU")
}

func TestResolvedATVoWiFiAdapterExecutesPhysicalVendorRadioDialect(t *testing.T) {
	ec20, err := vowifi.NewEC20Adapter(vowifiInventoryExecutor{}, vowifi.ATModemAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	zxic, err := vowifi.NewZXICAdapter(vowifiInventoryExecutor{}, vowifi.ATModemAdapterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range []string{"0536", "ffff"} {
		adapter := newResolvedATVoWiFiAdapter(store.DeviceTypePCIeEC20EC25, func(context.Context) (device.Device, error) {
			return device.Device{Discovered: true, Candidate: modem.Candidate{VendorID: "19d2", ProductID: product}}, nil
		}, ec20, zxic)
		if _, err := adapter.Snapshot(context.Background(), "legacy-pcie"); (err == nil) != (product == "0536") {
			t.Fatalf("product %s Snapshot error = %v, want tolerance only for verified ZXIC", product, err)
		}
	}
}

func TestResolvedATVoWiFiAdapterLookupCancellationDoesNotHoldCacheLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ec20, zxic := &vowifi.EC20Adapter{}, &vowifi.ZXICAdapter{}
		blocked, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		adapter := newResolvedATVoWiFiAdapter(store.DeviceTypePCIeEC20EC25, func(ctx context.Context) (device.Device, error) {
			if ctx == blocked {
				<-ctx.Done()
				return device.Device{}, ctx.Err()
			}
			return device.Device{Discovered: true, Candidate: modem.Candidate{VendorID: "19d2", ProductID: "0536"}}, nil
		}, ec20, zxic)
		done := make(chan struct{})
		go func() { adapter.current(blocked); close(done) }()
		synctest.Wait()
		if adapter.current(context.Background()) != zxic {
			t.Fatal("blocked lookup must not prevent another operation from selecting its adapter")
		}
		if blocked.Err() != nil {
			t.Fatal("another operation waited for the blocked lookup's deadline")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("lookup did not honor the operation deadline")
		}
	})
}
