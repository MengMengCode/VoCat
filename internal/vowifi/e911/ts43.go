package e911

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TS.43 sections 2.8 and 3: EAP-AKA relay authenticates the current SIM;
// ap2004 supplies the VoWiFi service websheet and authoritative sub-statuses.
// No generic HTTP success or websheet callback can establish E911 success.
func (c *EntitlementClient) refreshTS43(ctx context.Context) error {
	u, _ := url.Parse(c.endpoint)
	query := u.Query()
	query.Set("terminal_id", c.identity.IMEI)
	query.Set("terminal_vendor", "VoCat")
	query.Set("terminal_model", "Module")
	query.Set("terminal_sw_version", "1.0")
	query.Set("entitlement_version", "5.0")
	query.Set("vers", "0")
	query.Set("app", "ap2004")
	if c.token != "" {
		query.Set("IMSI", c.identity.IMSI)
		query.Set("token", c.token)
	} else {
		query.Set("EAP_ID", c.eap.Identity())
	}
	u.RawQuery = query.Encode()
	accept := "application/vnd.gsma.eap-relay.v1.0+json, application/json, text/vnd.wap.connectivity-xml"
	data, err := c.request(ctx, http.MethodGet, u.String(), nil, "", accept, false)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && (httpErr.Status == 401 || httpErr.Status == 511) && c.token != "" {
		c.token = ""
		query.Del("IMSI")
		query.Del("token")
		query.Set("EAP_ID", c.eap.Identity())
		u.RawQuery = query.Encode()
		data, err = c.request(ctx, http.MethodGet, u.String(), nil, "", accept, false)
	}
	if err != nil {
		return err
	}
	for round := 0; round < 5; round++ {
		var relay struct {
			Packet string `json:"eap-relay-packet"`
		}
		_ = json.Unmarshal(data, &relay)
		if relay.Packet == "" {
			return c.applyTS43(data)
		}
		packet, err := base64.StdEncoding.DecodeString(relay.Packet)
		if err != nil {
			return errors.New("carrier supplied an invalid EAP relay challenge")
		}
		answer, err := c.eap.Respond(ctx, packet)
		if err != nil || len(answer) == 0 {
			return errors.New("SIM could not authenticate the carrier EAP relay challenge")
		}
		data, err = c.request(ctx, http.MethodPost, c.endpoint, encodeJSON(map[string]string{"eap-relay-packet": base64.StdEncoding.EncodeToString(answer)}), "application/vnd.gsma.eap-relay.v1.0+json", accept, false)
		if err != nil {
			return err
		}
	}
	return errors.New("carrier EAP relay exceeded the retry limit")
}

func (c *EntitlementClient) applyTS43(data []byte) error {
	params, token, err := parseTS43(data)
	if err != nil {
		return err
	}
	if token != "" {
		c.token = token
	}
	c.status = ts43Status(params, time.Now())
	return c.setWebsheet(firstParameter(params, "serviceflowurl", "serverflowurl"), firstParameter(params, "serviceflowuserdata", "serverflowuserdata"))
}

func parameterKey(key string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r == ' ' {
			return -1
		}
		return r
	}, key))
}

func firstParameter(params map[string]string, names ...string) string {
	for _, name := range names {
		if value := params[name]; value != "" {
			return value
		}
	}
	return ""
}

func stringParameters(raw map[string]any) map[string]string {
	result := make(map[string]string)
	for name, value := range raw {
		switch v := value.(type) {
		case string:
			result[parameterKey(name)] = v
		case float64:
			result[parameterKey(name)] = strconv.FormatFloat(v, 'f', -1, 64)
		}
	}
	return result
}

func parseTS43(data []byte) (map[string]string, string, error) {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			return nil, "", errors.New("invalid carrier entitlement JSON")
		}
		var service map[string]any
		if json.Unmarshal(raw["ap2004"], &service) != nil || len(service) == 0 {
			return nil, "", errors.New("carrier omitted VoWiFi entitlement configuration")
		}
		var token struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(raw["Token"], &token)
		if token.Token == "" {
			_ = json.Unmarshal(raw["TOKEN"], &token)
		}
		return stringParameters(service), token.Token, nil
	}
	// Match APPLICATION by AppID, rather than collecting similarly named
	// fields from a different service (VoLTE, SMSoIP, or ODSA).
	decoder := xml.NewDecoder(bytes.NewReader(data))
	type characteristic struct {
		kind   string
		params map[string]string
	}
	var stack []*characteristic
	var params map[string]string
	token := ""
	for {
		node, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", errors.New("invalid carrier entitlement XML")
		}
		switch n := node.(type) {
		case xml.StartElement:
			if len(stack) > 32 {
				return nil, "", errors.New("carrier entitlement XML is too deeply nested")
			}
			switch n.Name.Local {
			case "characteristic":
				item := &characteristic{params: make(map[string]string)}
				for _, a := range n.Attr {
					if a.Name.Local == "type" {
						item.kind = strings.ToUpper(a.Value)
					}
				}
				stack = append(stack, item)
			case "parm":
				if len(stack) == 0 {
					continue
				}
				name, value := "", ""
				for _, a := range n.Attr {
					if a.Name.Local == "name" {
						name = parameterKey(a.Value)
					}
					if a.Name.Local == "value" {
						value = a.Value
					}
				}
				stack[len(stack)-1].params[name] = value
			}
		case xml.EndElement:
			if n.Name.Local == "characteristic" && len(stack) > 0 {
				item := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if item.kind == "APPLICATION" && item.params["appid"] == "ap2004" {
					if params != nil {
						return nil, "", errors.New("carrier returned duplicate VoWiFi entitlement configuration")
					}
					params = item.params
				}
				if item.kind == "TOKEN" {
					token = item.params["token"]
				}
			}
		}
	}
	if len(params) == 0 {
		return nil, "", errors.New("carrier omitted VoWiFi entitlement configuration")
	}
	return params, token, nil
}

func ts43Status(params map[string]string, now time.Time) Status {
	available := func(key string) bool { return params[key] == "1" || params[key] == "2" }
	status := Status{WiFiCalling: params["entitlementstatus"] == "1" && available("provstatus"), LocationAccepted: available("addrstatus"), TermsAccepted: available("tcstatus"), ExpiresAt: params["addrexpiry"], Confirmation: "carrier_status"}
	switch params["addrstatus"] {
	case "0":
		status.AddressStatus = "Missing"
	case "1":
		status.AddressStatus = "Available"
	case "2":
		status.AddressStatus = "NotRequired"
	case "3":
		status.AddressStatus = "Pending"
	}
	status.Confirmed = status.WiFiCalling && params["addrstatus"] == "1" && status.TermsAccepted
	if status.ExpiresAt != "" {
		expiry, err := time.Parse(time.RFC3339, status.ExpiresAt)
		if err != nil || !expiry.After(now) {
			status.Confirmed, status.LocationAccepted = false, false
		}
	}
	return status
}
