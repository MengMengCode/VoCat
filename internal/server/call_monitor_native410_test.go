package server

import (
	"context"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
	"vocat/internal/store"
)

func TestCellularCallMonitorSkipsNative410ButPollsSerialModems(t *testing.T) {
	for _, kind := range []string{store.DeviceTypeWiFi410, store.DeviceTypePCIeEC20EC25} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			database, err := store.Open(ctx, ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := database.UpsertDevice(ctx, store.Device{ID: "test-modem", Name: "Test", DeviceType: kind}); err != nil {
				t.Fatal(err)
			}
			queries := 0
			controller := fakeDeviceController{
				entry: device.Device{ID: "test-modem", Discovered: true},
				atHandler: func(command string) (modem.Response, error) {
					if command != "AT+CLCC" {
						t.Fatalf("unexpected command: %s", command)
					}
					queries++
					return modem.Response{Final: "OK"}, nil
				},
			}
			s := &Server{store: database, devices: controller}
			s.pollCellularCalls(ctx)
			want := 1
			if kind == store.DeviceTypeWiFi410 {
				want = 0
			}
			if queries != want {
				t.Fatalf("CLCC queries = %d, want %d", queries, want)
			}
		})
	}
}
