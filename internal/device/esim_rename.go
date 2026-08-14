package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrESIMNicknameTooLong         = errors.New("esim: profile nickname must not exceed 64 characters")
	ErrESIMNicknameProfileNotFound = errors.New("esim: profile to rename was not found on the eUICC")
)

func buildSetNicknameRequest(iccid, nickname string) ([]byte, error) {
	bcd, err := encodeICCID(strings.TrimSpace(iccid))
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(nickname) {
		return nil, errors.New("esim: profile nickname is not valid UTF-8")
	}
	if utf8.RuneCountInString(nickname) > 64 {
		return nil, ErrESIMNicknameTooLong
	}
	// SGP.22 ES10c SetNicknameRequest:
	// BF29 { 5A <ICCID BCD> 90 <UTF-8 nickname> }.
	return derConstruct(0xBF29, derEncode(0x5A, bcd), derEncode(0x90, []byte(nickname))), nil
}

func setNicknameResult(payload []byte) (byte, bool) {
	nodes := derParse(payload)
	if len(nodes) != 1 || nodes[0].tag != 0xBF29 {
		return 0, false
	}
	result := derFindValue(payload, 0x80)
	if len(result) != 1 {
		return 0, false
	}
	return result[0], true
}

// ESIMRenameProfile updates the on-card Profile Nickname through ES10c. It
// does not enable, disable, download, or delete a profile.
func (manager *Manager) ESIMRenameProfile(ctx context.Context, id, iccid, nickname, aidHex string) error {
	request, err := buildSetNicknameRequest(iccid, nickname)
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
	defer channel.close(context.Background())

	commitContext, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), csimAPDUTimeout)
	payload, err := channel.es10(commitContext, request)
	cancelCommit()
	if err != nil {
		return err
	}
	result, ok := setNicknameResult(payload)
	if !ok {
		return fmt.Errorf("esim: unexpected SetNickname response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	switch result {
	case 0:
		manager.renameCachedProfile(id, strings.TrimSpace(iccid), nickname)
		return nil
	case 1:
		return fmt.Errorf("%w (result=0x%02X)", ErrESIMNicknameProfileNotFound, result)
	default:
		return fmt.Errorf("esim: eUICC rejected SetNickname, result=0x%02X (raw %s)", result, strings.ToUpper(hex.EncodeToString(payload)))
	}
}
