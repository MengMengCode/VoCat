package e911

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vocat/internal/vowifi"
	"vocat/internal/vowifi/ike"
)

const nsdsURL = "https://sentitlement2.mobile.att.net/WFC"
const webOrigin = "https://attdashboard.wireless.att.com"

type Address struct {
	Street string `json:"street"`
	Unit   string `json:"unit"`
	City   string `json:"city"`
	State  string `json:"state"`
	ZIP    string `json:"zip"`
}

var states = strings.Fields("AL AK AZ AR CA CO CT DE DC FL GA HI ID IL IN IA KS KY LA ME MD MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC SD TN TX UT VT VA WA WV WI WY AS GU MP PR VI")
var zipPattern = regexp.MustCompile(`^\d{5}(-\d{4})?$`)

func (a Address) Normalize() (Address, error) {
	a.Street, a.Unit, a.City = strings.TrimSpace(a.Street), strings.TrimSpace(a.Unit), strings.TrimSpace(a.City)
	a.State, a.ZIP = strings.ToUpper(strings.TrimSpace(a.State)), strings.TrimSpace(a.ZIP)
	validState := false
	for _, state := range states {
		validState = validState || a.State == state
	}
	if a.Street == "" || len(a.Street) > 128 || len(a.Unit) > 64 || a.City == "" || len(a.City) > 64 || !validState || !zipPattern.MatchString(a.ZIP) {
		return Address{}, errors.New("enter a street address, city, two-letter US state and valid ZIP code")
	}
	for _, value := range []string{a.Street, a.Unit, a.City} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return Address{}, errors.New("address fields must contain a single line")
		}
	}
	return a, nil
}

type Status struct {
	Address          *Address `json:"address,omitempty"`
	AddressStatus    string   `json:"address_status"`
	WiFiCalling      bool     `json:"wifi_calling"`
	LocationAccepted bool     `json:"location_accepted"`
	TermsAccepted    bool     `json:"terms_accepted"`
	Confirmed        bool     `json:"confirmed"`
	Confirmation     string   `json:"confirmation,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
}

type Candidate struct {
	Address Address `json:"address"`
	Match   bool    `json:"match"`
}

// ATTClient is a short-lived, per-SIM session. Tokens, service fingerprints,
// cookies and graph headers are held only in memory and never returned to UI.
// Callers must serialize methods and verify the live ICCID before each action.
type ATTClient struct {
	http        *http.Client
	identity    vowifi.SIMIdentity
	eap         *ike.EAPAKAClient
	deviceID    string
	akaToken    string
	fingerprint string
	webToken    string
	graph       string
	reseller    string
	activate    bool
	addressID   string
	candidates  []map[string]any
	submitted   *Address
	Terms       string
	Status      Status
}

func NewATTClient(identity vowifi.SIMIdentity, aka vowifi.AKAProvider, client *http.Client) (*ATTClient, error) {
	if len(identity.IMEI) != 15 || strings.Trim(identity.IMEI, "0123456789") != "" {
		return nil, errors.New("the carrier requires the module's valid 15-digit IMEI")
	}
	eap, err := ike.NewEAPAKAClient(identity, aka)
	if err != nil {
		return nil, err
	}
	if client == nil {
		jar, _ := cookiejar.New(nil)
		client = &http.Client{Timeout: 20 * time.Second, Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 || req.URL.Scheme != "https" || req.URL.Host != "attdashboard.wireless.att.com" {
				return errors.New("carrier redirected outside its provisioning service")
			}
			return nil
		}}
	}
	urn := "urn:gsma:imei:" + identity.IMEI[:8] + "-" + identity.IMEI[8:14] + "-0"
	return &ATTClient{http: client, identity: identity, eap: eap, deviceID: base64.StdEncoding.EncodeToString([]byte(urn))}, nil
}

func (c *ATTClient) auth() map[string]any {
	request := map[string]any{"message-id": 1, "method": "3gppAuthentication", "device-id": c.deviceID, "device-type": 0, "os-type": 0, "device-name": "VoCat", "imsi-eap": c.eap.Identity()}
	if c.akaToken != "" {
		request["aka-token"] = c.akaToken
	}
	return request
}

type nsdsResponse struct {
	ID          int    `json:"message-id"`
	Code        int    `json:"response-code"`
	Challenge   string `json:"aka-challenge"`
	Token       string `json:"aka-token"`
	Fingerprint string `json:"service-fingerprint"`
	WebURL      string `json:"server-url"`
	WebData     string `json:"server-data"`
	Location    bool   `json:"location-status"`
	Terms       bool   `json:"tc-status"`
	Expiry      string `json:"aid-expiration"`
	Services    []struct {
		Name string `json:"service-name"`
		Code int    `json:"entitlement-status"`
	} `json:"service-entitlement"`
}

func (c *ATTClient) nsds(ctx context.Context, requests []map[string]any) ([]nsdsResponse, error) {
	for round := 0; round < 4; round++ {
		body, err := json.Marshal(requests)
		if err != nil {
			return nil, err
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write(body)
		_ = writer.Close()
		headers := http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}, "Accept": {"application/json"}, "Accept-Encoding": {"gzip"}, "X-Generic-Protocol-Version": {"1.0"}}
		response, _, err := c.request(ctx, http.MethodPost, nsdsURL, compressed.Bytes(), headers)
		if err != nil {
			return nil, err
		}
		var messages []nsdsResponse
		if err := json.Unmarshal(response, &messages); err != nil || len(messages) == 0 {
			return nil, errors.New("carrier returned an invalid entitlement response")
		}
		var authentication *nsdsResponse
		for i := range messages {
			if messages[i].ID == 1 {
				authentication = &messages[i]
			}
		}
		if authentication == nil {
			return nil, errors.New("carrier omitted SIM authentication status")
		}
		if authentication.Challenge != "" && authentication.Code == 1003 {
			packet, err := base64.StdEncoding.DecodeString(authentication.Challenge)
			if err != nil {
				return nil, errors.New("carrier supplied an invalid AKA challenge")
			}
			answer, err := c.eap.Respond(ctx, packet)
			if err != nil {
				return nil, errors.New("SIM could not authenticate the carrier challenge")
			}
			requests[0]["aka-challenge-rsp"] = base64.StdEncoding.EncodeToString(answer)
			if authentication.Token != "" {
				requests[0]["aka-token"] = authentication.Token
			}
			continue
		}
		for _, message := range messages {
			if message.Code != 1000 {
				return nil, fmt.Errorf("carrier rejected entitlement operation %d (code %d)", message.ID, message.Code)
			}
		}
		if authentication.Token != "" {
			c.akaToken = authentication.Token
		}
		return messages, nil
	}
	return nil, errors.New("carrier SIM authentication exceeded the retry limit")
}

func (c *ATTClient) query(ctx context.Context, method string, extra map[string]any) (nsdsResponse, error) {
	request := map[string]any{"message-id": 2, "method": method, "device-id": c.deviceID}
	for k, value := range extra {
		request[k] = value
	}
	messages, err := c.nsds(ctx, []map[string]any{c.auth(), request})
	if err != nil {
		return nsdsResponse{}, err
	}
	for _, message := range messages {
		if message.ID == 2 {
			return message, nil
		}
	}
	return nsdsResponse{}, errors.New("carrier omitted the requested operation result")
}

var tokenPattern = regexp.MustCompile(`var\s+SoftPhoneToken\s*=\s*("(?:[^"\\\r\n]|\\.)*")`)
var resellerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var scriptPattern = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(?:script|style)>`)
var tagPattern = regexp.MustCompile(`<[^>]*>`)

func (c *ATTClient) Start(ctx context.Context) error {
	if _, err := c.nsds(ctx, []map[string]any{c.auth()}); err != nil {
		return err
	}
	line, err := c.query(ctx, "getMSISDN", nil)
	if err != nil {
		return err
	}
	if line.Fingerprint == "" {
		return errors.New("carrier did not supply a line service fingerprint")
	}
	c.fingerprint = line.Fingerprint
	location, err := c.query(ctx, "manageLocationAndTC", map[string]any{"service-fingerprint": c.fingerprint})
	if err != nil {
		return err
	}
	u, err := url.Parse(location.WebURL)
	if err != nil || u.Scheme != "https" || u.Host != "attdashboard.wireless.att.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("carrier did not supply a trusted emergency address portal")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "softphone" || parts[1] != "primary" || (len(parts) == 3 && !resellerPattern.MatchString(parts[2])) || location.WebData == "" {
		return errors.New("carrier emergency address portal is not supported")
	}
	c.reseller = "softphone"
	if len(parts) == 3 {
		c.reseller = parts[2]
	}
	form := url.Values{"token": {location.WebData}}
	body, _, err := c.request(ctx, http.MethodPost, u.String(), []byte(form.Encode()), http.Header{"Content-Type": {"application/x-www-form-urlencoded"}})
	if err != nil {
		return err
	}
	match := tokenPattern.FindSubmatch(body)
	if len(match) != 2 {
		return errors.New("carrier portal did not accept the provisioning token")
	}
	c.webToken, err = decodePortalToken(match[1])
	if err != nil {
		return errors.New("carrier portal supplied an invalid provisioning token")
	}
	// Match the official portal's initialization sequence, including graph headers.
	if _, err := c.web(ctx, http.MethodGet, "/sfservice/v1/appinfo/"+c.reseller, nil, false); err != nil {
		return err
	}
	if err := c.Refresh(ctx); err != nil {
		return err
	}
	terms, err := c.web(ctx, http.MethodGet, "/sfservice/v1/information/important", nil, true)
	if err != nil {
		return err
	}
	c.Terms = strings.Join(strings.Fields(html.UnescapeString(tagPattern.ReplaceAllString(scriptPattern.ReplaceAllString(string(terms), " "), " "))), " ")
	if c.Terms == "" {
		return errors.New("carrier did not supply Wi-Fi Calling terms")
	}
	return nil
}

// The carrier embeds a JavaScript string, not JSON: production responses use
// ECMAScript identity escapes such as \- as well as standard JSON escapes.
// Decode that string without evaluating any carrier-supplied JavaScript.
func decodePortalToken(quoted []byte) (string, error) {
	var normalized strings.Builder
	for i := 0; i < len(quoted); i++ {
		ch := quoted[i]
		if ch != '\\' {
			normalized.WriteByte(ch)
			continue
		}
		i++
		if i >= len(quoted) {
			return "", errors.New("incomplete string escape")
		}
		escaped := quoted[i]
		switch {
		case strings.ContainsRune(`"\/bfnrtu`, rune(escaped)):
			normalized.WriteByte('\\')
			normalized.WriteByte(escaped)
		case escaped == 'x':
			if i+2 >= len(quoted) {
				return "", errors.New("incomplete hex escape")
			}
			if _, err := strconv.ParseUint(string(quoted[i+1:i+3]), 16, 8); err != nil {
				return "", err
			}
			normalized.WriteString(`\u00`)
			normalized.Write(quoted[i+1 : i+3])
			i += 2
		case escaped == 'v' || (escaped >= '0' && escaped <= '9'):
			return "", errors.New("control or octal escape in token")
		default:
			normalized.WriteByte(escaped)
		}
	}
	var token string
	if err := json.Unmarshal([]byte(normalized.String()), &token); err != nil {
		return "", err
	}
	if token == "" || len(token) > 32768 {
		return "", errors.New("invalid token length")
	}
	for _, ch := range token {
		if ch < 32 || ch == 127 {
			return "", errors.New("control character in token")
		}
	}
	return token, nil
}

func (c *ATTClient) Refresh(ctx context.Context) error {
	service, err := c.query(ctx, "serviceEntitlementStatus", map[string]any{"service-list": []string{"vowifi"}})
	if err != nil {
		return err
	}
	c.Status.WiFiCalling = false
	for _, item := range service.Services {
		if item.Name == "vowifi" && item.Code == 1000 {
			c.Status.WiFiCalling = true
		}
	}
	location, err := c.query(ctx, "manageLocationAndTC", map[string]any{"service-fingerprint": c.fingerprint})
	if err != nil {
		return err
	}
	c.Status.LocationAccepted, c.Status.TermsAccepted, c.Status.ExpiresAt = location.Location, location.Terms, location.Expiry
	eligibility, err := c.web(ctx, http.MethodGet, "/sfservice/v1/eligibility", nil, true)
	if err != nil {
		return err
	}
	var eligible struct {
		Activate bool `json:"activate"`
	}
	if json.Unmarshal(eligibility, &eligible) != nil {
		return errors.New("invalid carrier eligibility response")
	}
	c.activate = eligible.Activate
	c.Status.Address, c.Status.AddressStatus, c.addressID = nil, "", ""
	if !c.activate {
		current, err := c.web(ctx, http.MethodGet, "/sfservice/v1/address/e911", nil, true)
		if err != nil {
			// An activated line with no E911 address can legitimately return 404.
			var carrierErr *HTTPError
			if !errors.As(err, &carrierErr) || carrierErr.Status != http.StatusNotFound {
				return err
			}
		} else {
			var record struct {
				ID      string         `json:"addressId"`
				Status  string         `json:"status"`
				Address map[string]any `json:"address"`
			}
			if json.Unmarshal(current, &record) != nil {
				return errors.New("invalid carrier address response")
			}
			if record.ID != "" && record.Address != nil {
				a := addressFromCarrier(record.Address)
				c.Status.Address, c.Status.AddressStatus, c.addressID = &a, record.Status, record.ID
			}
		}
	}
	c.Status.Confirmed = c.Status.WiFiCalling && c.Status.LocationAccepted && c.Status.TermsAccepted && strings.EqualFold(c.Status.AddressStatus, "Validated")
	if c.submitted != nil && (c.Status.Address == nil || !sameAddress(*c.Status.Address, *c.submitted)) {
		c.Status.Confirmed = false
	}
	return nil
}

func (c *ATTClient) Verify(ctx context.Context, address Address) ([]Candidate, error) {
	c.candidates = nil
	address, err := address.Normalize()
	if err != nil {
		return nil, err
	}
	body, err := c.web(ctx, http.MethodPut, "/sfservice/v1/address/e911/verify", map[string]any{"address1": address.Street, "address2": address.Unit, "city": address.City, "state": address.State, "zipCode": address.ZIP}, true)
	if err != nil {
		return nil, err
	}
	var response struct {
		Addresses []map[string]any `json:"addrOut"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Addresses) == 0 {
		return nil, errors.New("carrier could not validate this address; check street, unit and ZIP code")
	}
	if len(response.Addresses) > 20 {
		return nil, errors.New("carrier returned too many address matches")
	}
	c.candidates = response.Addresses
	result := make([]Candidate, 0, len(c.candidates))
	for _, raw := range c.candidates {
		match, _ := raw["requestMatch"].(bool)
		result = append(result, Candidate{Address: addressFromCarrier(raw), Match: match})
	}
	return result, nil
}

func (c *ATTClient) Submit(ctx context.Context, index int, accepted bool) (Status, error) {
	if !accepted {
		return c.Status, errors.New("accept the carrier's Wi-Fi Calling terms before submitting")
	}
	if index < 0 || index >= len(c.candidates) {
		return c.Status, errors.New("validate and select an address first")
	}
	chosen := c.candidates[index]
	expected := addressFromCarrier(chosen)
	if c.activate {
		if _, err := c.web(ctx, http.MethodPut, "/sfservice/v1/device/primary/activate", nil, true); err != nil {
			return c.Status, err
		}
		c.activate = false
	}
	payload := make(map[string]any)
	for _, field := range []string{"address1", "address2", "city", "csiAddress", "state", "streetDirection", "streetName", "streetNumber", "streetTrailingDirection", "streetType", "unitType", "unitValue", "zipCode"} {
		if value, ok := chosen[field]; ok {
			payload[field] = value
		}
	}
	path := "/sfservice/v1/address/e911"
	if c.addressID != "" {
		path += "/update"
		payload["addressId"] = c.addressID
		payload["alternativeDeviceId"] = ""
	}
	if _, err := c.web(ctx, http.MethodPut, path, payload, true); err != nil {
		return c.Status, err
	}
	c.submitted = &expected
	if err := c.Refresh(ctx); err != nil {
		return c.Status, errors.New("address was submitted, but carrier confirmation could not be read; refresh the session to check")
	}
	// A successful HTTP write alone is not proof of registration. Require the
	// carrier's current address to match the selected, validated candidate.
	if c.Status.Address == nil || !sameAddress(*c.Status.Address, expected) {
		c.Status.Confirmed = false
	}
	return c.Status, nil
}

func addressFromCarrier(raw map[string]any) Address {
	get := func(key string) string { value, _ := raw[key].(string); return value }
	return Address{Street: get("address1"), Unit: get("address2"), City: get("city"), State: get("state"), ZIP: get("zipCode")}
}

func sameAddress(a, b Address) bool {
	return strings.EqualFold(strings.TrimSpace(a.Street), strings.TrimSpace(b.Street)) && strings.EqualFold(strings.TrimSpace(a.Unit), strings.TrimSpace(b.Unit)) && strings.EqualFold(a.City, b.City) && strings.EqualFold(a.State, b.State) && a.ZIP == b.ZIP
}

type HTTPError struct {
	Status int
	Code   string
}

func (err *HTTPError) Error() string {
	if err.Code != "" {
		return fmt.Sprintf("carrier portal rejected the request (HTTP %d, %s)", err.Status, err.Code)
	}
	return fmt.Sprintf("carrier portal rejected the request (HTTP %d)", err.Status)
}

func (c *ATTClient) web(ctx context.Context, method, path string, payload any, reseller bool) ([]byte, error) {
	if reseller && c.reseller != "softphone" {
		path += "/reseller/" + c.reseller
	}
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	headers := http.Header{"Token": {c.webToken}, "Accept": {"application/json, text/plain, */*"}}
	if c.graph != "" {
		headers.Set("Graph", c.graph)
	}
	if payload != nil {
		headers.Set("Content-Type", "application/json")
	}
	response, h, err := c.request(ctx, method, webOrigin+path, body, headers)
	if graph := h.Get("Graph"); graph != "" {
		c.graph = graph
	}
	return response, err
}

func (c *ATTClient) request(ctx context.Context, method, endpoint string, body []byte, headers http.Header) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("could not prepare carrier request")
	}
	req.Header = headers
	req.Header.Set("User-Agent", "VoCat/1.0")
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, errors.New("could not reach the carrier provisioning service; check network connectivity or retry")
	}
	defer response.Body.Close()
	var reader io.Reader = response.Body
	if strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip") {
		unzip, err := gzip.NewReader(response.Body)
		if err != nil {
			return nil, nil, errors.New("invalid compressed carrier response")
		}
		defer unzip.Close()
		reader = unzip
	}
	data, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, errors.New("carrier response could not be read")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var message struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(data, &message)
		if !regexp.MustCompile(`^[A-Z]{2,8}[0-9]{2,8}$`).MatchString(message.ID) {
			message.ID = ""
		}
		return nil, response.Header, &HTTPError{Status: response.StatusCode, Code: message.ID}
	}
	return data, response.Header, nil
}
