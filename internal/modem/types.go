package modem

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUnsupportedPlatform = errors.New("modem: platform is not supported")
	ErrSessionClosed       = errors.New("modem: AT session is closed")
	ErrCommandTimeout      = errors.New("modem: AT command timed out")
	ErrPromptNotReceived   = errors.New("modem: command completed without a prompt")
)

// PortRole describes the conventional role of a Quectel USB serial interface.
// The role is a discovery hint; only a successful AT probe proves that a port is
// usable.
type PortRole string

const (
	PortRoleUnknown    PortRole = "unknown"
	PortRoleDiagnostic PortRole = "diagnostic"
	PortRoleNMEA       PortRole = "nmea"
	PortRoleAT         PortRole = "at"
	PortRoleModem      PortRole = "modem"
)

type Port struct {
	Path            string   `json:"path"`
	StablePath      string   `json:"stablePath,omitempty"`
	Name            string   `json:"name"`
	InterfaceNumber int      `json:"interfaceNumber"`
	Role            PortRole `json:"role"`
}

func (p Port) OpenPath() string {
	if strings.TrimSpace(p.StablePath) != "" {
		return p.StablePath
	}
	return p.Path
}

type Candidate struct {
	HardwareKind string `json:"hardwareKind,omitempty"`
	ReaderName   string `json:"readerName,omitempty"`
	ID           string `json:"id"`
	VendorID     string `json:"vendorId"`
	ProductID    string `json:"productId"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	USBPath      string `json:"usbPath"`
	// USBGeneration changes whenever Linux enumerates the same physical USB
	// path again (busnum:devnum). It is runtime-only and lets higher layers
	// discard file descriptors opened against the previous device instance.
	USBGeneration    string `json:"-"`
	ATPort           Port   `json:"atPort"`
	Ports            []Port `json:"ports"`
	QMIControl       string `json:"qmiControl,omitempty"`
	NetworkInterface string `json:"networkInterface,omitempty"`
	DiscoveryIssue   string `json:"discoveryIssue,omitempty"`
}

func (c Candidate) HasATPort() bool {
	return strings.TrimSpace(c.ATPort.OpenPath()) != ""
}

type Discoverer interface {
	Discover(context.Context) ([]Candidate, error)
}

// Response is the response belonging to exactly one AT command. URCs that
// arrived between the command echo and final result are returned separately and
// also retained by the session's URC queue.
type Response struct {
	Command  string        `json:"command"`
	Lines    []string      `json:"lines"`
	Final    string        `json:"final"`
	URCs     []string      `json:"urcs,omitempty"`
	Duration time.Duration `json:"duration"`
}

func (r Response) Text() string {
	return strings.Join(r.Lines, "\n")
}

func (r Response) OK() bool {
	return strings.EqualFold(strings.TrimSpace(r.Final), "OK")
}

type CommandError struct {
	Command string
	Final   string
	Lines   []string
}

func (e *CommandError) Error() string {
	detail := strings.TrimSpace(strings.Join(e.Lines, "; "))
	if detail == "" {
		return fmt.Sprintf("%s failed: %s", e.Command, e.Final)
	}
	return fmt.Sprintf("%s failed: %s (%s)", e.Command, e.Final, detail)
}

// Client is the device layer's narrow AT dependency. Session implements it;
// tests can supply a deterministic transcript without opening hardware.
type Client interface {
	Execute(context.Context, string) (Response, error)
	WaitURC(context.Context, func(string) bool) (string, error)
	Close() error
}

// PromptClient performs the two-phase interaction used by AT+CMGS. The
// implementation must hold the same command serialization lock while waiting
// for the prompt, writing payload+Ctrl-Z, and reading the final result.
type PromptClient interface {
	Client
	ExecutePrompt(context.Context, string, []byte) (Response, error)
}

type Opener interface {
	Open(context.Context, Port) (Client, error)
}
