// Package mcp exposes Vocat operations as Model Context Protocol tools for
// AI agents. It consumes the same seams the HTTP API and the Telegram bot
// use — the device manager, the VoWiFi controllers and the store — and must
// never import internal/server (the HTTP server can mount this package, so
// the dependency may only flow one way).
package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"vocat/internal/device"
	"vocat/internal/store"
	"vocat/internal/vowifi"
	"vocat/internal/vowifi/integration"
)

// DeviceManager is the hardware boundary the tools consume; device.Manager
// and the HTTP layer's DeviceController satisfy it.
type DeviceManager interface {
	List() []device.Device
	Get(string) (device.Device, error)
	Refresh(context.Context, string) (device.Snapshot, error)
	SendSMS(context.Context, string, string, string) (device.SMSSendResult, error)
	ESIMListProfiles(context.Context, string) (device.EsimInfo, error)
	ESIMSwitchProfile(context.Context, string, string, string) error
}

// VoWiFiController is the asynchronous VoWiFi runtime boundary; state
// transitions continue after a tool call returns.
type VoWiFiController interface {
	State(string) (vowifi.State, error)
	RequestReconnect(string) (vowifi.State, error)
}

type Options struct {
	Store   *store.Store
	Devices DeviceManager
	VoWiFi  VoWiFiController
	Logger  *slog.Logger
	Version string
	// ReadOnly registers only the read tools. The HTTP mount builds one
	// read-write and one read-only server and picks per bearer token, so a
	// read-only agent never even sees the write tools in its tool list.
	ReadOnly bool
	// SwitchESIM performs the complete configured-device operation, including
	// runtime maintenance, RF policy and target card-policy restoration. The
	// low-level DeviceManager switch is deliberately not a safe fallback.
	SwitchESIM func(context.Context, string, string, string) error
}

// Server wraps an MCP SDK server bound to the Vocat service seams.
type Server struct {
	options Options
	sdk     *mcp.Server
}

func New(options Options) (*Server, error) {
	if options.Store == nil {
		return nil, errors.New("mcp: store is required")
	}
	if options.Devices == nil {
		return nil, errors.New("mcp: device manager is required")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	implementation := &mcp.Implementation{Name: "vocat", Title: "Vocat"}
	if options.Version != "" {
		implementation.Version = options.Version
	}
	server := &Server{
		options: options,
		sdk:     mcp.NewServer(implementation, nil),
	}
	server.registerTools()
	return server, nil
}

// Handler serves the Streamable HTTP transport for remote agents. Callers
// are responsible for authentication; Vocat mounts it behind bearer-token
// authentication on the existing listener.
func (s *Server) Handler() http.Handler {
	const responseDeadline = 3 * time.Minute
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.sdk }, &mcp.StreamableHTTPOptions{
		// Zero would keep idle sessions in the handler's map forever; a
		// client that never sends DELETE would leak it for the process
		// lifetime. The SDK pauses the timer while a request is in flight,
		// so long eSIM operations are not cut short.
		SessionTimeout: 10 * time.Minute,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The shared listener caps writes at 30s; eSIM operations can run
		// for minutes, and a cut-off response after a completed switch
		// invites the agent to repeat the operation.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(responseDeadline))
		streamable.ServeHTTP(w, r)
	})
}

// resolvePhysical maps a configured device ID to the live modem using the
// shared scored matcher, so MCP tool calls address exactly the same hardware
// as the web UI.
func (s *Server) resolvePhysical(ctx context.Context, configuredID string) (string, error) {
	physicalID, err := integration.ResolvePhysicalID(ctx, s.options.Store, s.options.Devices, configuredID)
	if err != nil {
		return "", fmt.Errorf("device %q is not online: %w", configuredID, err)
	}
	return physicalID, nil
}

// audit records a mutating tool call in the same audit trail the web UI
// writes to, with the actor identifying the MCP surface.
func (s *Server) audit(ctx context.Context, action string, entityID string) {
	if _, err := s.options.Store.AppendAuditEvent(ctx, store.AuditEvent{
		Actor:      "mcp",
		Action:     action,
		EntityType: "device",
		EntityID:   entityID,
		Outcome:    "success",
		RemoteAddr: "mcp",
	}); err != nil {
		s.options.Logger.Warn("mcp audit event failed", "action", action, "error", err)
	}
}
