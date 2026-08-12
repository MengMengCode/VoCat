package device

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// EsimNotification is one notification retained by an eUICC until its receiver
// acknowledges it through ES9+.HandleNotification.
type EsimNotification struct {
	SequenceNumber uint64 `json:"sequenceNumber"`
	Event          string `json:"event,omitempty"`
	ICCID          string `json:"iccid,omitempty"`
	Address        string `json:"address,omitempty"`
	AIDHex         string `json:"aidHex,omitempty"`
	CanRetry       bool   `json:"canRetry"`

	raw []byte
}

func encodePositiveInteger(value uint64) []byte {
	if value == 0 {
		return []byte{0}
	}
	encoded := make([]byte, 8)
	for index := len(encoded) - 1; index >= 0; index-- {
		encoded[index] = byte(value & 0xff)
		value >>= 8
	}
	for len(encoded) > 1 && encoded[0] == 0 {
		encoded = encoded[1:]
	}
	if encoded[0]&0x80 != 0 {
		encoded = append([]byte{0}, encoded...)
	}
	return encoded
}

func decodePositiveInteger(encoded []byte) (uint64, bool) {
	if len(encoded) == 0 || len(encoded) > 9 || encoded[0]&0x80 != 0 {
		return 0, false
	}
	if len(encoded) == 9 {
		if encoded[0] != 0 {
			return 0, false
		}
		encoded = encoded[1:]
	}
	var value uint64
	for _, octet := range encoded {
		value = value<<8 | uint64(octet)
	}
	return value, true
}

func buildRetrieveNotificationsRequest(sequenceNumber *uint64) []byte {
	if sequenceNumber == nil {
		return derConstruct(0xBF2B)
	}
	return derConstruct(0xBF2B, derEncode(0x80, encodePositiveInteger(*sequenceNumber)))
}

func buildListNotificationsRequest() []byte {
	return derConstruct(0xBF28)
}

func buildRemoveNotificationRequest(sequenceNumber uint64) []byte {
	return derConstruct(0xBF30, derEncode(0x80, encodePositiveInteger(sequenceNumber)))
}

func notificationEventName(bitString []byte) string {
	if len(bitString) < 2 || bitString[0] > 7 {
		return ""
	}
	bitCount := (len(bitString)-1)*8 - int(bitString[0])
	for bit := 0; bit < bitCount; bit++ {
		if bitString[1+bit/8]&(0x80>>uint(bit%8)) == 0 {
			continue
		}
		switch bit {
		case 0:
			return "install"
		case 1, 4:
			return "enable"
		case 2, 5:
			return "disable"
		case 3, 6:
			return "delete"
		case 7:
			return "rpm"
		default:
			return fmt.Sprintf("event-%d", bit)
		}
	}
	return ""
}

func notificationFromMetadata(metadata *derNode) (EsimNotification, error) {
	sequenceNumber, ok := decodePositiveInteger(derValue(metadata.children, 0x80))
	if !ok {
		return EsimNotification{}, errors.New("esim: pending notification has an invalid sequence number")
	}
	address := strings.TrimSpace(string(derValue(metadata.children, 0x0C)))
	if address == "" {
		return EsimNotification{}, errors.New("esim: pending notification has no receiver address")
	}
	return EsimNotification{
		SequenceNumber: sequenceNumber,
		Event:          notificationEventName(derValue(metadata.children, 0x81)),
		ICCID:          decodeICCID(derValue(metadata.children, 0x5A)),
		Address:        address,
		CanRetry:       true,
	}, nil
}

func parsePendingNotification(raw []byte) (EsimNotification, error) {
	metadataNodes := derFindAll(derParse(raw), 0xBF2F)
	if len(metadataNodes) == 0 {
		return EsimNotification{}, errors.New("esim: pending notification has no metadata")
	}
	notification, err := notificationFromMetadata(metadataNodes[0])
	if err != nil {
		return EsimNotification{}, err
	}
	notification.raw = append([]byte(nil), raw...)
	return notification, nil
}

func parseNotificationMetadataList(payload []byte) ([]EsimNotification, error) {
	tag, headerLength, totalLength, err := derElementAt(payload, 0)
	if err != nil || tag != 0xBF28 || totalLength != len(payload) {
		return nil, fmt.Errorf("esim: unexpected ListNotification response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	value := payload[headerLength:totalLength]
	responseNodes := derParse(value)
	if len(responseNodes) == 1 && (responseNodes[0].tag == 0x81 || responseNodes[0].tag == 0x80 || responseNodes[0].tag == 0x02) {
		return nil, fmt.Errorf("esim: eUICC could not list notifications (result %X)", responseNodes[0].value)
	}
	metadataNodes := derFindAll(responseNodes, 0xBF2F)
	notifications := make([]EsimNotification, 0, len(metadataNodes))
	for _, metadata := range metadataNodes {
		notification, parseErr := notificationFromMetadata(metadata)
		if parseErr != nil {
			return nil, parseErr
		}
		notifications = append(notifications, notification)
	}
	sort.SliceStable(notifications, func(left, right int) bool {
		if notifications[left].Address == notifications[right].Address {
			return notifications[left].SequenceNumber < notifications[right].SequenceNumber
		}
		return notifications[left].Address < notifications[right].Address
	})
	return notifications, nil
}

func parsePendingNotifications(payload []byte) ([]EsimNotification, error) {
	tag, headerLength, totalLength, err := derElementAt(payload, 0)
	if err != nil || tag != 0xBF2B || totalLength != len(payload) {
		return nil, fmt.Errorf("esim: unexpected RetrieveNotificationsList response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	value := payload[headerLength:totalLength]
	responseNodes := derParse(value)
	if len(responseNodes) == 1 && (responseNodes[0].tag == 0x81 || responseNodes[0].tag == 0x80 || responseNodes[0].tag == 0x02) {
		errorCode := responseNodes[0].value
		return nil, fmt.Errorf("esim: eUICC could not retrieve notifications (result %X)", errorCode)
	}
	// The notificationList CHOICE alternative is encoded as context tag A0 by
	// AUTOMATIC TAGS on newer eUICCs. Older cards are also seen returning the
	// SEQUENCE OF contents directly. Accept both without including the list
	// wrapper in the PendingNotification sent to ES9+.
	if len(responseNodes) == 1 && responseNodes[0].tag == 0xA0 {
		value = responseNodes[0].value
	} else if len(responseNodes) == 1 && responseNodes[0].tag == 0x30 && firstChild(responseNodes[0].children, 0xBF2F) == nil {
		value = responseNodes[0].value
	}

	var notifications []EsimNotification
	for offset := 0; offset < len(value); {
		_, _, elementLength, elementErr := derElementAt(value, offset)
		if elementErr != nil {
			return nil, elementErr
		}
		raw := value[offset : offset+elementLength]
		notification, parseErr := parsePendingNotification(raw)
		if parseErr != nil {
			return nil, parseErr
		}
		notifications = append(notifications, notification)
		offset += elementLength
	}
	sort.SliceStable(notifications, func(left, right int) bool {
		if notifications[left].Address == notifications[right].Address {
			return notifications[left].SequenceNumber < notifications[right].SequenceNumber
		}
		return notifications[left].Address < notifications[right].Address
	})
	return notifications, nil
}

func removeNotificationResult(payload []byte) error {
	roots := derParse(payload)
	if len(roots) != 1 || roots[0].tag != 0xBF30 {
		return fmt.Errorf("esim: unexpected RemoveNotificationFromList response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	result := derValue(roots[0].children, 0x80)
	if len(result) == 0 {
		result = derValue(roots[0].children, 0x02)
	}
	if len(result) != 1 {
		return fmt.Errorf("esim: malformed RemoveNotificationFromList response %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	switch result[0] {
	case 0, 1: // ok, or already removed after an earlier acknowledged retry
		return nil
	default:
		return fmt.Errorf("esim: eUICC could not remove notification (result %d)", result[0])
	}
}

func (channel *euiccChannel) retrieveNotifications(ctx context.Context, sequenceNumber *uint64) ([]EsimNotification, error) {
	payload, err := channel.es10(ctx, buildRetrieveNotificationsRequest(sequenceNumber))
	if err != nil {
		return nil, err
	}
	return parsePendingNotifications(payload)
}

func (channel *euiccChannel) listNotifications(ctx context.Context) ([]EsimNotification, error) {
	payload, err := channel.es10(ctx, buildListNotificationsRequest())
	if err != nil {
		return nil, err
	}
	return parseNotificationMetadataList(payload)
}

func (channel *euiccChannel) removeNotification(ctx context.Context, sequenceNumber uint64) error {
	payload, err := channel.es10(ctx, buildRemoveNotificationRequest(sequenceNumber))
	if err != nil {
		return err
	}
	return removeNotificationResult(payload)
}

func (channel *euiccChannel) deliverNotification(ctx context.Context, notification EsimNotification) error {
	client, err := newES9PClient(ctx, notification.Address)
	if err != nil {
		return err
	}
	if err := client.handleNotification(ctx, notification.raw); err != nil {
		return err
	}
	if err := channel.removeNotification(ctx, notification.SequenceNumber); err != nil {
		return fmt.Errorf("notification acknowledged but could not be removed from eUICC: %w", err)
	}
	return nil
}

// deliverPendingNotifications sends each receiver's notifications oldest first.
// A failed item stops only that receiver's group so a later sequence number can
// never overtake it and make the older notification stale.
func (channel *euiccChannel) deliverPendingNotifications(ctx context.Context) error {
	notifications, err := channel.listNotifications(ctx)
	if err != nil {
		return err
	}
	blockedAddresses := make(map[string]bool)
	var failures []error
	for _, notification := range notifications {
		if blockedAddresses[notification.Address] {
			continue
		}
		pending, retrieveErr := channel.retrieveNotifications(ctx, &notification.SequenceNumber)
		if retrieveErr == nil {
			retrieveErr = fmt.Errorf("esim: notification %d was not returned by eUICC", notification.SequenceNumber)
			for _, candidate := range pending {
				if candidate.SequenceNumber == notification.SequenceNumber {
					retrieveErr = channel.deliverNotification(ctx, candidate)
					break
				}
			}
		}
		if retrieveErr != nil {
			blockedAddresses[notification.Address] = true
			failures = append(failures, fmt.Errorf("notification %d to %s: %w", notification.SequenceNumber, notification.Address, retrieveErr))
		}
	}
	return errors.Join(failures...)
}

// ESIMNotifications returns the notifications retained across every eUICC
// storage exposed by the physical card.
func (manager *Manager) ESIMNotifications(ctx context.Context, id string) ([]EsimNotification, error) {
	manager.esimMu.Lock()
	defer manager.esimMu.Unlock()
	if err := manager.waitForESIMRecovery(ctx, id); err != nil {
		return nil, err
	}

	var all []EsimNotification
	var lastErr error
	succeeded := false
	for _, aid := range manager.discoverEuiccAIDs(ctx, id) {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			lastErr = err
			continue
		}
		notifications, retrieveErr := channel.listNotifications(ctx)
		channel.close(context.Background())
		if retrieveErr != nil {
			lastErr = retrieveErr
			continue
		}
		succeeded = true
		for index := range notifications {
			notifications[index].AIDHex = aid
		}
		all = append(all, notifications...)
	}
	if !succeeded && lastErr != nil {
		return nil, lastErr
	}
	return all, nil
}

// ESIMRetryNotification sends one retained notification and removes it from the
// eUICC only after the receiver returns the SGP.22 success acknowledgement.
func (manager *Manager) ESIMRetryNotification(ctx context.Context, id, aidHex string, sequenceNumber uint64) error {
	manager.esimMu.Lock()
	defer manager.esimMu.Unlock()
	if err := manager.waitForESIMRecovery(ctx, id); err != nil {
		return err
	}
	channel, err := manager.openEuiccAID(ctx, id, targetEuiccAID(aidHex))
	if err != nil {
		return err
	}
	defer channel.close(context.Background())
	notifications, err := channel.retrieveNotifications(ctx, &sequenceNumber)
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		if notification.SequenceNumber == sequenceNumber {
			return channel.deliverNotification(ctx, notification)
		}
	}
	return fmt.Errorf("esim: notification %d was not found", sequenceNumber)
}
