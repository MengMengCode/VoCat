package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrESIMDisableProfileNotFound        = errors.New("esim: profile to disable was not found on the eUICC")
	ErrESIMProfileNotEnabled             = errors.New("esim: profile is not currently enabled")
	ErrESIMDisableDisallowedByPolicy     = errors.New("esim: profile disabling is not allowed by its policy")
	ErrESIMDisableCATBusy                = errors.New("esim: card application toolkit is busy; retry disabling later")
	ErrESIMDisableDisallowedByEnterprise = errors.New("esim: profile disabling is not allowed by enterprise rule")
	ErrESIMDisableCommandError           = errors.New("esim: eUICC internal command error (commandError) while disabling profile")
	ErrESIMDisableDisallowedForRPM       = errors.New("esim: profile disabling is disallowed for roaming position management")
	ErrESIMDisableUndefined              = errors.New("esim: eUICC returned undefinedError while disabling profile")
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

// buildDisableProfileRequestWithAID constructs an ES10c DisableProfileRequest
// with ISD-P AID addressing (tag 4F) per GSMA SGP.22 clause 5.7.19:
//   BF32 { A0 { 4F <AID> } 81 01 FF }
func buildDisableProfileRequestWithAID(aid []byte) ([]byte, error) {
	if len(aid) == 0 || len(aid) > 16 {
		return nil, fmt.Errorf("esim: invalid ISD-P AID length %d (must be 1-16 octets)", len(aid))
	}
	profileID := derConstruct(0xA0, derEncode(0x4F, aid))
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
	wrap := func(cause error) error {
		return fmt.Errorf("%w (result=0x%02X, raw %s)", cause, result, raw)
	}
	switch result {
	case 0:
		return nil
	case 1:
		return wrap(ErrESIMDisableProfileNotFound)
	case 2:
		return wrap(ErrESIMProfileNotEnabled)
	case 3:
		return wrap(ErrESIMDisableDisallowedByPolicy)
	case 5:
		return wrap(ErrESIMDisableCATBusy)
	case 6:
		return wrap(ErrESIMDisableDisallowedByEnterprise)
	case 7:
		return wrap(ErrESIMDisableCommandError)
	case 9:
		return wrap(ErrESIMDisableDisallowedForRPM)
	case 0x7F:
		return wrap(ErrESIMDisableUndefined)
	default:
		return fmt.Errorf("esim: eUICC rejected DisableProfile, result=0x%02X (raw %s)", result, raw)
	}
}

// ESIMDisableProfile disables the currently enabled profile through ES10c.
// With refreshFlag=true, the modem must be reset/re-discovered after commit.
func (manager *Manager) ESIMDisableProfile(ctx context.Context, id, iccid, aidHex string) error {
	var request []byte
	var err error
	if isHexAID(iccid) {
		aidBytes, hexErr := hex.DecodeString(strings.TrimSpace(iccid))
		if hexErr != nil {
			return fmt.Errorf("esim: decode ISD-P AID %q: %w", iccid, hexErr)
		}
		request, err = buildDisableProfileRequestWithAID(aidBytes)
	} else {
		request, err = buildDisableProfileRequest(iccid)
	}
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

	result, ok := disableProfileResult(payload)
	// Fallback to ISD-P AID addressing if ICCID was rejected with commandError (0x07),
	// undefinedError (0x7F), or notFound (0x01).
	if err == nil && ok && !isHexAID(iccid) && (result == 7 || result == 0x7F || result == 1) {
		if aidStr := manager.resolveProfileAID(ctx, id, channel, iccid); aidStr != "" {
			if aidBytes, decErr := hex.DecodeString(aidStr); decErr == nil && len(aidBytes) > 0 {
				if aidReq, buildErr := buildDisableProfileRequestWithAID(aidBytes); buildErr == nil {
					aidCommitContext, cancelAIDCommit := context.WithTimeout(context.WithoutCancel(ctx), csimAPDUTimeout)
					aidPayload, aidErr := channel.es10(aidCommitContext, aidReq)
					cancelAIDCommit()
					if aidErr == nil {
						if aidRes, aidOk := disableProfileResult(aidPayload); aidOk {
							payload = aidPayload
							result = aidRes
							ok = aidOk
						}
					}
				}
			}
		}
	}

	closeContext, cancelClose := context.WithTimeout(context.Background(), csimAPDUTimeout)
	channel.close(closeContext)
	cancelClose()
	if err != nil {
		// The card may have committed immediately before a transport failure.
		manager.startProfileSwitchRecovery(id)
		return err
	}
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
