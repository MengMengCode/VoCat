package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/device"
	"vocat/internal/modem"
)

type esimInventoryErrorController struct {
	fakeDeviceController
	inventoryErr error
}

func (controller esimInventoryErrorController) ESIMInventory(context.Context, string) ([]device.EsimInventoryEntry, error) {
	return nil, controller.inventoryErr
}

func TestESIMOverviewDistinguishesMissingCardFromProbeFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "ordinary_SIM", err: fmt.Errorf("esim: open application: %w", device.ErrNoEUICC), wantStatus: http.StatusOK},
		{name: "probe_open_deadline", err: fmt.Errorf("esim: open temporary AT channel: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout, wantCode: "modem_timeout"},
		{name: "probe_close_timeout", err: fmt.Errorf("esim: close temporary AT channel 2: %w", modem.ErrCommandTimeout), wantStatus: http.StatusGatewayTimeout, wantCode: "modem_timeout"},
		{name: "probe_canceled", err: fmt.Errorf("esim: open temporary AT channel: %w", context.Canceled), wantStatus: http.StatusRequestTimeout, wantCode: "request_canceled"},
		{name: "probe_close_rejected", err: errors.New("esim: close temporary AT channel 2 (SW=6A81)"), wantStatus: http.StatusBadGateway, wantCode: "modem_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{logger: regionTestLogger(), devices: esimInventoryErrorController{inventoryErr: test.err}}
			recorder := httptest.NewRecorder()
			server.writeEsimOverview(recorder, httptest.NewRequest(http.MethodGet, "/esim", nil), "dev1", true)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if test.wantStatus == http.StatusOK {
				data := decodeData(t, recorder)
				profiles, ok := data["profiles"].([]any)
				if data["chipInfo"] != nil || !ok || len(profiles) != 0 {
					t.Fatalf("ordinary SIM should return an empty overview: %#v", data)
				}
				return
			}
			var envelope errorEnvelope
			if err := json.NewDecoder(recorder.Body).Decode(&envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.wantCode {
				t.Fatalf("error code = %q, want %q", envelope.Error.Code, test.wantCode)
			}
		})
	}
}
