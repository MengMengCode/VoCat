package pcsc

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const HardwareKind = "pcsc"

var (
	ErrUnsupported     = errors.New("pcsc: platform is not supported")
	ErrUnavailable     = errors.New("pcsc: service is unavailable")
	ErrReaderNotFound  = errors.New("pcsc: reader not found")
	ErrNoCard          = errors.New("pcsc: no card is inserted")
	ErrPINRequired     = errors.New("pcsc: SIM PIN is required")
	ErrPINTriesLow     = errors.New("pcsc: refusing PIN verification because too few attempts remain")
	ErrPINRejected     = errors.New("pcsc: SIM PIN was rejected")
	ErrUSIMUnavailable = errors.New("pcsc: no usable USIM application was found")
	ErrCardChanged     = errors.New("pcsc: card identity changed during authentication")
	ErrAKARejected     = errors.New("pcsc: USIM rejected the network authentication token")
)

type Reader struct {
	Name         string
	USBPath      string
	VendorID     string
	ProductID    string
	Manufacturer string
	Product      string
	CardPresent  bool
	ATR          string
	// DiscoveryIssue is set when USB sees a smart-card reader but pcscd cannot
	// expose it yet. Keeping the physical reader visible lets the UI explain the
	// missing service/driver instead of silently showing an empty device list.
	DiscoveryIssue string
}

type Selector struct {
	USBPath    string
	ReaderName string
}

func (selector Selector) validate() error {
	if strings.TrimSpace(selector.USBPath) == "" && strings.TrimSpace(selector.ReaderName) == "" {
		return errors.New("pcsc: reader selector is empty")
	}
	return nil
}

type Identity struct {
	ICCID       string
	IMSI        string
	MNCLength   int
	USIMAID     []byte
	SMSC        string
	SPN         string
	PINRequired bool
	PINTries    int
}

type Snapshot struct {
	Reader   Reader
	Identity Identity
}

type AKAChallenge struct {
	RAND [16]byte
	AUTN [16]byte
}

type AKAResult struct {
	RES                    []byte
	CK                     []byte
	IK                     []byte
	AUTS                   []byte
	SynchronizationFailure bool
}

type PINError struct {
	Kind  error
	Tries int
}

func (err *PINError) Error() string {
	if err == nil {
		return "pcsc: SIM PIN error"
	}
	if err.Tries >= 0 {
		return fmt.Sprintf("%v (%d attempts remain)", err.Kind, err.Tries)
	}
	return err.Kind.Error()
}

func (err *PINError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Kind
}

type Card interface {
	Transmit(context.Context, []byte) ([]byte, uint16, error)
	Close() error
}

type Backend interface {
	Readers(context.Context) ([]Reader, error)
	Open(context.Context, Selector) (Card, error)
}
