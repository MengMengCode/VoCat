package e911

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// MSP v2 is used by US carrier bundles. The SIM response is a complete EAP
// packet, not raw RES, and its length must include the variable-length NAI.
func (c *EntitlementClient) mspIdentity() string {
	nai := []byte(c.eap.Identity())
	packet := make([]byte, 5+len(nai))
	packet[0], packet[1], packet[4] = 2, 0, 1 // EAP Response / Identity
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[5:], nai)
	return base64.StdEncoding.EncodeToString(packet)
}

type mspMessage struct {
	ID        int              `json:"response-id"`
	Status    json.RawMessage  `json:"status"`
	Challenge string           `json:"challenge"`
	Token     string           `json:"token"`
	Response  []map[string]any `json:"response"`
}

func (c *EntitlementClient) msp(ctx context.Context, requests []map[string]any) ([]mspMessage, error) {
	data, err := c.request(ctx, http.MethodPost, c.endpoint, encodeJSON(requests), "application/json", "application/json", true)
	if err != nil {
		return nil, err
	}
	var result []mspMessage
	if json.Unmarshal(data, &result) != nil || len(result) == 0 {
		return nil, errors.New("carrier returned invalid MSP entitlement messages")
	}
	return result, nil
}

func mspResult(messages []mspMessage, id int) (mspMessage, error) {
	var result *mspMessage
	for i := range messages {
		if messages[i].ID == id {
			if result != nil {
				return mspMessage{}, errors.New("carrier returned duplicate entitlement operation results")
			}
			result = &messages[i]
		}
	}
	if result == nil {
		return mspMessage{}, errors.New("carrier omitted the requested entitlement operation")
	}
	return *result, nil
}

func mspSuccess(status json.RawMessage) bool {
	return string(status) == "6000" || string(status) == `"6000"` || string(status) == `"STATUS_SUCCESS"`
}

func (c *EntitlementClient) refreshMSP(ctx context.Context) error {
	auth := map[string]any{"action-name": "getAuthentication", "request-id": 1, "auth-type": "EAP-AKA", "subscriber-id": c.mspIdentity(), "unique-id": c.deviceID, "device-account-identifier": c.accountID}
	if c.token != "" {
		auth["token"] = c.token
	}
	service := map[string]any{"action-name": "getEntitlement", "request-id": 2, "entitlement-names": []string{"VoWiFi"}}
	// Authenticate before asking for the service, retaining the server's
	// cookies across every challenge and token-backed query.
	for round := 0; round < 5; round++ {
		messages, err := c.msp(ctx, []map[string]any{auth, service})
		if err != nil {
			return err
		}
		result, err := mspResult(messages, 1)
		if err != nil {
			return err
		}
		if result.Challenge != "" {
			packet, err := base64.StdEncoding.DecodeString(result.Challenge)
			if err != nil {
				return errors.New("carrier supplied an invalid MSP AKA challenge")
			}
			answer, err := c.eap.Respond(ctx, packet)
			if err != nil || len(answer) == 0 {
				return errors.New("SIM could not authenticate the carrier MSP challenge")
			}
			auth = map[string]any{"action-name": "postChallenge", "request-id": 1, "payload": base64.StdEncoding.EncodeToString(answer)}
			continue
		}
		if !mspSuccess(result.Status) {
			// One token expiry retry is allowed; do not loop on rejected SIMs.
			if round == 0 && c.token != "" {
				c.token = ""
				delete(auth, "token")
				continue
			}
			return errors.New("carrier rejected SIM entitlement authentication; use the carrier account or a compatible phone")
		}
		if result.Token != "" {
			c.token = result.Token
		}
		entitlement, err := mspResult(messages, 2)
		if err != nil {
			return err
		}
		if !mspSuccess(entitlement.Status) {
			return errors.New("carrier did not allow VoWiFi entitlement for this line or module")
		}
		return c.applyMSP(entitlement.Response)
	}
	return errors.New("carrier MSP SIM authentication exceeded the retry limit")
}

func (c *EntitlementClient) applyMSP(services []map[string]any) error {
	for _, service := range services {
		name, _ := service["entitlement-name"].(string)
		if !strings.EqualFold(name, "VoWiFi") {
			continue
		}
		properties, _ := service["service-property"].(map[string]any)
		params := stringParameters(properties)
		// Some carriers embed the standardized sub-statuses. Do not interpret
		// a generic entitlement-enabled response as a confirmed E911 address.
		c.status = ts43Status(params, time.Now())
		if !c.status.WiFiCalling {
			c.status.WiFiCalling = service["entitlement-status"] == "STATUS_ENABLED_ENTITLEMENT"
		}
		rawURL := firstParameter(params, "serviceflowurl", "serverflowurl", "websheeturl")
		data := firstParameter(params, "serviceflowuserdata", "serverflowuserdata", "websheetdata")
		if err := c.setWebsheet(rawURL, data); err != nil {
			return err
		}
		if c.portal == nil && !c.status.Confirmed {
			return errors.New("carrier did not return a supported E911 registration websheet; use its account portal or phone settings")
		}
		return nil
	}
	return errors.New("carrier omitted the VoWiFi entitlement result")
}
