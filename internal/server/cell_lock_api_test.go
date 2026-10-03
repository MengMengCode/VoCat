package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"vocat/internal/device"
)

type cellLockTestController struct {
	fakeDeviceController
	target   *device.CellLockTarget
	err      error
	setCalls int
}

func (f *cellLockTestController) Cells(context.Context, string) (device.CellList, error) {
	return device.CellList{
		Items:           []device.CellInfo{{CellLockTarget: device.CellLockTarget{1650, 0}, Source: "serving"}},
		NeighborsStatus: "unavailable",
	}, nil
}

func (f *cellLockTestController) CellLock(context.Context, string) (device.CellLockStatus, error) {
	return device.CellLockStatus{Target: f.target}, f.err
}

func (f *cellLockTestController) SetCellLock(_ context.Context, _ string, target *device.CellLockTarget) (device.CellLockStatus, error) {
	f.target = target
	f.setCalls++
	return device.CellLockStatus{Target: target}, f.err
}

func TestCellLockAPI(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		code, writes             int
		failure                  error
		target                   *device.CellLockTarget
	}{
		{name: "cells without lock support", method: "GET", path: "/cells", code: 200, failure: device.ErrUnsupportedCapability},
		{name: "status", method: "GET", path: "/cell-lock", code: 200, target: &device.CellLockTarget{1650, 0}},
		{name: "lock zero values", method: "PUT", path: "/cell-lock", body: `{"earfcn":0,"pci":0}`, code: 200, writes: 1, target: &device.CellLockTarget{}},
		{name: "clear", method: "DELETE", path: "/cell-lock", code: 200, writes: 1},
		{name: "missing PCI", method: "PUT", path: "/cell-lock", body: `{"earfcn":1650}`, code: 400},
		{name: "invalid PCI", method: "PUT", path: "/cell-lock", body: `{"earfcn":1650,"pci":504}`, code: 400},
		{name: "unsupported method", method: "POST", path: "/cell-lock", code: 405},
		{name: "unsupported hardware", method: "GET", path: "/cell-lock", code: 501, failure: device.ErrUnsupportedCapability},
		{name: "write failed", method: "DELETE", path: "/cell-lock", code: 502, writes: 1, failure: errors.New("readback mismatch")},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := &cellLockTestController{target: &device.CellLockTarget{1650, 0}, err: test.failure}
			s := &Server{devices: controller, logger: regionTestLogger(), maxRequestBodyBytes: 4096}
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.path == "/cells" {
				s.handleCells(response, request, "physical")
			} else {
				s.handleCellLock(response, request, "dev1", "physical")
			}
			if response.Code != test.code || controller.setCalls != test.writes {
				t.Fatalf("status=%d writes=%d body=%s", response.Code, controller.setCalls, response.Body)
			}
			if test.writes != 0 && !reflect.DeepEqual(controller.target, test.target) {
				t.Fatalf("forwarded target=%+v, want %+v", controller.target, test.target)
			}
			if test.code != 200 {
				return
			}
			data := decodeData(t, response)
			if test.path == "/cells" {
				items, ok := data["items"].([]any)
				if !ok || len(items) != 1 {
					t.Fatalf("cells response=%+v", data)
				}
				cell, ok := items[0].(map[string]any)
				if !ok || cell["earfcn"] != float64(1650) || cell["pci"] != float64(0) {
					t.Fatalf("cell target must remain flat JSON, including zero PCI: %+v", items[0])
				}
				return
			}
			var want any
			if test.target != nil {
				want = map[string]any{"earfcn": float64(test.target.EARFCN), "pci": float64(test.target.PCI)}
			}
			if got, present := data["target"]; !present || !reflect.DeepEqual(got, want) {
				t.Fatalf("target response=%+v, want %+v", data, want)
			}
		})
	}
}
