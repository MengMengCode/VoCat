package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"vocat/internal/modem"
)

type busyTelegramCLCCController struct {
	fakeDeviceController
	capability func(context.Context) (bool, error)
}

func (c busyTelegramCLCCController) SupportsCLCC(ctx context.Context, _ string) (bool, error) {
	return c.capability(ctx)
}

func TestTelegramCellularCapabilitySharesOperationBudget(t *testing.T) {
	for _, action := range []string{"status", "dial", "answer", "hangup", "answer-confirm", "hangup-confirm"} {
		for _, canceled := range []bool{false, true} {
			t.Run(action+map[bool]string{true: "/cancel", false: "/timeout"}[canceled], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if canceled {
						go func() { time.Sleep(time.Second); cancel() }()
					}
					var commands []string
					checks := 0
					controller := busyTelegramCLCCController{
						capability: func(ctx context.Context) (bool, error) {
							checks++
							if strings.HasSuffix(action, "-confirm") && checks == 1 {
								return true, nil
							}
							<-ctx.Done()
							return true, ctx.Err()
						},
						fakeDeviceController: fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
							commands = append(commands, command)
							return modem.Response{Final: "OK"}, nil
						}},
					}
					bot := &telegramBot{server: &Server{devices: controller}}
					started := time.Now()
					var err error
					if action == "dial" {
						_, err = bot.executeTimedCellularCall(ctx, telegramRuntimeConfig{}, telegramPendingAction{Argument: "123", Duration: time.Second}, "physical")
					} else {
						_, err = bot.executeSimpleCellularCallAction(ctx, "physical", strings.TrimSuffix(action, "-confirm"))
					}
					want := map[string]time.Duration{"status": 8 * time.Second, "dial": 20 * time.Second, "answer": 20 * time.Second, "hangup": 4 * time.Second, "answer-confirm": 8 * time.Second, "hangup-confirm": 4 * time.Second}[action]
					wantErr := context.DeadlineExceeded
					if canceled {
						want, wantErr = time.Second, context.Canceled
					}
					if !errors.Is(err, wantErr) || time.Since(started) != want {
						t.Fatalf("elapsed=%s error=%v; want %s, %v", time.Since(started), err, want, wantErr)
					}
					wantCommands := ""
					if strings.HasPrefix(action, "hangup") {
						wantCommands = "ATH"
					} else if action == "answer-confirm" {
						wantCommands = "ATA"
					}
					if strings.Join(commands, ",") != wantCommands {
						t.Fatalf("commands=%v, want %q", commands, wantCommands)
					}
				})
			})
		}
	}
}

func TestTelegramTimedCellularCallReleasesBusyCapabilityAtHangup(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel", false: "duration"}[canceled], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if canceled {
					go func() { time.Sleep(500 * time.Millisecond); cancel() }()
				}
				checks := 0
				var dialAt, hangupAt time.Time
				controller := busyTelegramCLCCController{
					capability: func(ctx context.Context) (bool, error) {
						checks++
						if checks == 1 {
							return true, nil
						}
						<-ctx.Done()
						return true, ctx.Err()
					},
					fakeDeviceController: fakeDeviceController{atHandler: func(command string) (modem.Response, error) {
						switch command {
						case "ATD123;":
							dialAt = time.Now()
						case "ATH":
							hangupAt = time.Now()
						default:
							t.Fatalf("unexpected AT command %s", command)
						}
						return modem.Response{Final: "OK"}, nil
					}},
				}
				bot := &telegramBot{server: &Server{devices: controller}}
				_, err := bot.executeTimedCellularCall(ctx, telegramRuntimeConfig{BaseURL: "://invalid"}, telegramPendingAction{Argument: "123", Duration: time.Second}, "physical")
				want := time.Second
				if canceled {
					want = 500 * time.Millisecond
				}
				if hangupAt.Sub(dialAt) != want {
					t.Fatalf("hangup after %s, want %s", hangupAt.Sub(dialAt), want)
				}
				if (!canceled && err != nil) || (canceled && !errors.Is(err, context.Canceled)) {
					t.Fatalf("call error=%v", err)
				}
			})
		})
	}
}
