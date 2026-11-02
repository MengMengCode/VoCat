package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"vocat/internal/device"
)

func TestCellularDataRuntimeAllowsLongATSetup(t *testing.T) {
	for _, backend := range []string{"at", ""} {
		t.Run("backend="+backend, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setter := &runtimeNetworkSetter{apply: func(ctx context.Context, request device.NetworkRequest) (device.NetworkResult, error) {
					// A slow attach that is valid within the device's 90s budget.
					select {
					case <-time.After(80 * time.Second):
						return device.NetworkResult{Enabled: true, Backend: "at"}, nil
					case <-ctx.Done():
						return device.NetworkResult{}, ctx.Err()
					}
				}}
				runtime := newCellularDataRuntime(setter, nil)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				runtime.start(ctx)
				request := runtime.request("device", "physical", device.NetworkRequest{Enabled: true, Backend: backend})
				status, err := runtime.wait(ctx, "device", request.Revision)
				if err != nil || !status.Connected {
					t.Fatalf("slow AT setup: status=%+v, err=%v", status, err)
				}
			})
		})
	}
}

func TestCellularDataRuntimeKeepsQMISetupBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setter := &runtimeNetworkSetter{apply: func(ctx context.Context, _ device.NetworkRequest) (device.NetworkResult, error) {
			<-ctx.Done()
			return device.NetworkResult{}, ctx.Err()
		}}
		runtime := newCellularDataRuntime(setter, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runtime.start(ctx)
		started := time.Now()
		request := runtime.request("device", "physical", device.NetworkRequest{Enabled: true, Backend: "qmi"})
		_, err := runtime.wait(ctx, "device", request.Revision)
		if err == nil || time.Since(started) != 75*time.Second {
			t.Fatalf("QMI setup bound: elapsed=%s, err=%v", time.Since(started), err)
		}
	})
}

func TestCellularDataRuntimeCancelsLongATSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setter := &runtimeNetworkSetter{started: make(chan bool, 1), apply: func(ctx context.Context, _ device.NetworkRequest) (device.NetworkResult, error) {
			<-ctx.Done()
			return device.NetworkResult{}, ctx.Err()
		}}
		runtime := newCellularDataRuntime(setter, nil)
		ctx, cancel := context.WithCancel(context.Background())
		runtime.start(ctx)
		request := runtime.request("device", "physical", device.NetworkRequest{Enabled: true, Backend: "at"})
		<-setter.started
		cancel()
		synctest.Wait()
		status, err := runtime.wait(context.Background(), "device", request.Revision)
		if err == nil || status.Connected {
			t.Fatalf("canceled AT setup: status=%+v, err=%v", status, err)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("fixture context was not canceled")
		}
	})
}
