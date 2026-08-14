package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrESIMDisableProfileNotFound    = errors.New("esim: profile to disable was not found on the eUICC")
	ErrESIMProfileNotEnabled         = errors.New("esim: profile is not currently enabled")
	ErrESIMDisableDisallowedByPolicy = errors.New("esim: profile disabling is not allowed by its policy")
	ErrESIMDisableCATBusy            = errors.New("esim: card application toolkit is busy; retry disabling later")
)

func buildDisableProfileRequest(iccid string) ([]byte, error) {
	bcd, err := encodeICCID(strings.TrimSpace(iccid))
	if err != nil {
		return nil, err
	}
	// SGP.22 ES10c DisableProfileRequest:
	// BF32 { A0 { 5A <ICCID BCD> } 81 01 FF } (refreshFlag = true).
	profileID := derConstruct(0xA0, derEncode(0x5A, bcd))
	return derConstruct(0xBF32, profileID, derEncode(0x81, []byte{0xFF})), nil
}

func disableProfileResult(payload []byte) (byte, bool) {
	nodes := derParse(payload)
	if len(nodes) != 1 || nodes[0].tag != 0xBF32 {
		return 0, false
	}
	result := derFindValue(payload, 0x80)
	if len(result) != 1 {
		return 0, false
	}
	return result[0], true
}

func disableProfileResponseError(result byte, payload []byte) error {
	raw := strings.ToUpper(hex.EncodeToString(payload))
	switch result {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%w (result=0x%02X, raw %s)", ErrESIMDisableProfileNotFound, result, raw)
	case 2:
		return fmt.Errorf("%w (result=0x%02X, raw %s)", ErrESIMProfileNotEnabled, result, raw)
	case 3:
		return fmt.Errorf("%w (result=0x%02X, raw %s)", ErrESIMDisableDisallowedByPolicy, result, raw)
	case 5:
		return fmt.Errorf("%w (result=0x%02X, raw %s)", ErrESIMDisableCATBusy, result, raw)
	default:
		return fmt.Errorf("esim: eUICC rejected DisableProfile, result=0x%02X (raw %s)", result, raw)
	}
}

// ESIMDisableProfile disables the currently enabled profile through ES10c.
// With refreshFlag=true, the modem must be reset/re-discovered after commit.
func (manager *Manager) ESIMDisableProfile(ctx context.Context, id, iccid, aidHex string) error {
	request, err := buildDisableProfileRequest(iccid)
	if err != nil {
		return err
	}
	manager.lockESIM()
	defer manager.unlockESIM()
	if err := manager.waitForESIMRecovery(ctx, id); err != nil {
		return err
	}
	channel, err := manager.openEuiccAID(ctx, id, targetEuiccAID(aidHex))
	if err != nil {
		return err
	}

	commitContext, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), csimAPDUTimeout)
	payload, err := channel.es10(commitContext, request)
	cancelCommit()
	closeContext, cancelClose := context.WithTimeout(context.Background(), csimAPDUTimeout)
	channel.close(closeContext)
	cancelClose()
	if err != nil {
		// The card may have committed immediately before a transport failure.
		manager.startProfileSwitchRecovery(id)
		return err
	}
	result, ok := disableProfileResult(payload)
	if !ok {
		manager.startProfileSwitchRecovery(id)
		return fmt.Errorf("esim: unexpected DisableProfile response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	if err := disableProfileResponseError(result, payload); err != nil {
		return err
	}
	manager.markCachedProfileDisabled(id, strings.TrimSpace(iccid))
	manager.startProfileSwitchRecovery(id)
	return nil
}
