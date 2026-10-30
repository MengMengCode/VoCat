package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"vocat/internal/device"
	"vocat/internal/store"
)

type readonlyGETAuditController struct {
	fakeDeviceController
	downloads int
	scans     int
}

func (c *readonlyGETAuditController) ESIMDownloadProfile(context.Context, string, device.EsimDownloadParams, func(device.EsimProgress)) (*device.EsimDownloadResult, error) {
	c.downloads++
	return &device.EsimDownloadResult{}, nil
}

func (c *readonlyGETAuditController) ScanOperators(context.Context, string) (device.OperatorScanResult, error) {
	c.scans++
	return device.OperatorScanResult{}, nil
}

func TestReadOnlyBearerRejectsMutatingGETRoutesBeforeHardware(t *testing.T) {
	_, authentication, database := newBearerTestApplication(t)
	reader, err := authentication.CreateAPIToken(context.Background(), "audit-reader", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "EC20", ModemIMEI: "867394042309830"}); err != nil {
		t.Fatal(err)
	}
	controller := &readonlyGETAuditController{fakeDeviceController: fakeDeviceController{entry: device.Device{
		ID: "dev1", Discovered: true, Snapshot: &device.Snapshot{IMEI: "867394042309830", IMSI: "234150000000001"},
	}}}
	s := &Server{auth: authentication, store: database, devices: controller, logger: regionTestLogger()}
	for _, path := range []string{
		"/api/devices/dev1/esim/actions/download?smdp=example.test&matching_id=test",
		"/api/devices/dev1/operator_selection/scan",
		"/api/devices/dev1/operator_selection/scan/stream",
		"/api/devices/dev1/calls/media?call_id=active-call",
		"/api/devices/dev1/esim/actions/%2564ownload?smdp=example.test",
		"/api/devices/dev1/calls/%256dedia?call_id=active-call",
		"/api/devices/dev1/esim/actions/download/?smdp=example.test",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+reader.Token)
			recorder := httptest.NewRecorder()
			s.handleAPI(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Errorf("GET mutation returned %d, want 403: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if controller.downloads != 0 || controller.scans != 0 {
		t.Fatalf("read-only token reached hardware: downloads=%d scans=%d", controller.downloads, controller.scans)
	}
}

func TestReadWriteBearerRetainsLegacyMutatingGETRoutes(t *testing.T) {
	_, authentication, database := newBearerTestApplication(t)
	writer, err := authentication.CreateAPIToken(context.Background(), "audit-writer", false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertDevice(context.Background(), store.Device{ID: "dev1", Name: "EC20", ModemIMEI: "867394042309830"}); err != nil {
		t.Fatal(err)
	}
	controller := &readonlyGETAuditController{fakeDeviceController: fakeDeviceController{entry: device.Device{
		ID: "dev1", Discovered: true, Snapshot: &device.Snapshot{IMEI: "867394042309830", IMSI: "234150000000001"},
	}}}
	s := &Server{auth: authentication, store: database, devices: controller, logger: regionTestLogger()}
	for _, path := range []string{
		"/api/devices/dev1/esim/actions/download?smdp=example.test&matching_id=test",
		"/api/devices/dev1/operator_selection/scan",
		"/api/devices/dev1/operator_selection/scan/stream",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+writer.Token)
		recorder := httptest.NewRecorder()
		s.handleAPI(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Errorf("GET mutation returned %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
	}
	if controller.downloads != 1 || controller.scans != 2 {
		t.Fatalf("writer routing changed: downloads=%d scans=%d", controller.downloads, controller.scans)
	}
}
