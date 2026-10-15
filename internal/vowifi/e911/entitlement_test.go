package e911

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type entitlementTransport func(*http.Request) (*http.Response, error)

func (f entitlementTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func entitlementResponse(r *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func entitlementIdentity() vowifi.SIMIdentity {
	return vowifi.SIMIdentity{IMSI: "310280000000001", ICCID: "test-card", IMEI: "123456789012345", HomeMCC: "310", HomeMNC: "280"}
}

func TestTS43RelayUsesSIMAndPreservesCookies(t *testing.T) {
	aka := &testAKA{}
	calls := 0
	transport := entitlementTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Method != "GET" || r.URL.Query().Get("app") != "ap2004" || r.URL.Query().Get("EAP_ID") == "" {
				t.Fatal("missing initial TS.43 identity/service")
			}
			response := entitlementResponse(r, `{"eap-relay-packet":"`+testChallenge+`"}`)
			response.Header.Set("Set-Cookie", "carrier-session=synthetic; Secure; HttpOnly; Path=/")
			return response, nil
		}
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/vnd.gsma.eap-relay.v1.0+json" {
			t.Fatal("wrong relay method or media type")
		}
		cookie, err := r.Cookie("carrier-session")
		if err != nil || cookie.Value != "synthetic" {
			t.Fatal("authentication session cookie lost")
		}
		var packet map[string]string
		_ = json.NewDecoder(r.Body).Decode(&packet)
		if packet["eap-relay-packet"] != testAnswer {
			t.Fatal("incorrect SIM AKA relay answer")
		}
		return entitlementResponse(r, `{"Token":{"token":"private-test-token"},"ap2004":{"EntitlementStatus":0,"AddrStatus":0,"TC_Status":0,"ProvStatus":0,"ServiceFlow_URL":"https://e911.t-mobile.com/address","ServiceFlow_UserData":"token=a%2Bb%26c&line=synthetic"}}`), nil
	})
	c, err := NewEntitlementClient(entitlementIdentity(), aka, Carrier{Method: "ts43_websheet", Network: "tmobile"}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if aka.calls != 1 || calls != 2 || c.Snapshot().Confirmed || c.Websheet() == nil || c.Websheet().Fields.Get("token") != "a+b&c" {
		t.Fatal("incorrect TS.43 session/portal result")
	}
}

func TestTS43ConfigurationAndConfirmation(t *testing.T) {
	jsonConfig := `{"ap2003":{"EntitlementStatus":1,"AddrStatus":1},"ap2004":{"EntitlementStatus":"1","AddrStatus":"1","TC_Status":"1","ProvStatus":"1","AddrExpiry":"2099-01-01T00:00:00Z"}}`
	xmlConfig := `<wap-provisioningdoc><characteristic type="APPLICATION"><parm name="AppID" value="ap2003"/><parm name="AddrStatus" value="0"/></characteristic><characteristic type="APPLICATION"><parm name="AppID" value="ap2004"/><parm name="EntitlementStatus" value="1"/><parm name="AddrStatus" value="1"/><parm name="TC_Status" value="1"/><parm name="ProvStatus" value="1"/><parm name="ServiceFlow_URL" value="https://e911.t-mobile.com/address"/><parm name="ServiceFlow_UserData" value="token=x%2By&amp;line=synthetic"/></characteristic></wap-provisioningdoc>`
	for _, input := range []string{jsonConfig, xmlConfig} {
		params, _, err := parseTS43([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		status := ts43Status(params, time.Now())
		if !status.Confirmed || status.Confirmation != "carrier_status" || status.Address != nil {
			t.Fatalf("incorrect carrier status: %#v", status)
		}
		for _, field := range []string{"entitlementstatus", "addrstatus", "tcstatus", "provstatus"} {
			value := params[field]
			delete(params, field)
			if ts43Status(params, time.Now()).Confirmed {
				t.Fatalf("missing %s accepted", field)
			}
			params[field] = value
		}
		params["addrstatus"] = "2"
		if ts43Status(params, time.Now()).Confirmed {
			t.Fatal("address not required incorrectly treated as registered")
		}
		params["addrstatus"], params["addrexpiry"] = "1", "2000-01-01T00:00:00Z"
		if ts43Status(params, time.Now()).Confirmed {
			t.Fatal("expired emergency address accepted")
		}
	}
	for _, input := range []string{`{"ap2003":{"EntitlementStatus":1}}`, `<wap-provisioningdoc><characteristic type="APPLICATION"><parm name="AppID" value="ap2004"/></characteristic><characteristic type="APPLICATION"><parm name="AppID" value="ap2004"/></characteristic></wap-provisioningdoc>`} {
		if _, _, err := parseTS43([]byte(input)); err == nil {
			t.Fatal("ambiguous/unrelated entitlement accepted")
		}
	}
}

func TestMSPIdentityChallengeAndPortal(t *testing.T) {
	aka := &testAKA{}
	calls := 0
	transport := entitlementTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("X-Protocol-Version") != "2" {
			t.Fatal("missing MSP version")
		}
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		var requests []map[string]any
		if json.NewDecoder(reader).Decode(&requests) != nil || len(requests) != 2 {
			t.Fatal("invalid MSP batch")
		}
		if calls == 1 {
			packet, _ := base64.StdEncoding.DecodeString(requests[0]["subscriber-id"].(string))
			if len(packet) < 5 || int(binary.BigEndian.Uint16(packet[2:4])) != len(packet) || packet[4] != 1 {
				t.Fatal("invalid EAP identity packet")
			}
			return entitlementResponse(r, `[{"response-id":1,"challenge":"`+testChallenge+`"}]`), nil
		}
		if requests[0]["action-name"] != "postChallenge" || requests[0]["payload"] != testAnswer {
			t.Fatal("incorrect MSP SIM challenge response")
		}
		return entitlementResponse(r, `[{"response-id":1,"status":6000,"token":"private-test-token"},{"response-id":2,"status":6000,"response":[{"entitlement-name":"VoWiFi","entitlement-status":"STATUS_ENABLED_ENTITLEMENT","service-property":{"ServiceFlow_URL":"https://spg.vzw.com/SSFGateway/e911Location/changeAddress","ServiceFlow_UserData":"token=synthetic"}}]}]`), nil
	})
	c, err := NewEntitlementClient(entitlementIdentity(), aka, Carrier{ID: "visible", Method: "msp_websheet", Network: "verizon"}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.endpoint, "spc.myvzw.com") {
		t.Fatal("incorrect primary Verizon entitlement endpoint")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || aka.calls != 1 || c.Websheet() == nil || c.Snapshot().Confirmed {
		t.Fatal("generic entitlement treated as E911 confirmation")
	}
	if _, err := c.Submit(context.Background(), 0, true); err == nil {
		t.Fatal("local submit falsely accepted")
	}
}

func TestEntitlementPortalsAndResponseLimits(t *testing.T) {
	c, err := NewEntitlementClient(entitlementIdentity(), &testAKA{}, Carrier{Method: "msp_websheet", Network: "verizon"}, &http.Client{Transport: entitlementTransport(func(r *http.Request) (*http.Response, error) {
		return entitlementResponse(r, strings.Repeat("x", (1<<20)+1)), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://spg.vzw.com/address", "https://spg.vzw.com.evil.test/address", "https://spg.vzw.com@evil.test/", "https://127.0.0.1/", "https://spg.vzw.com:8443/"} {
		if err := c.setWebsheet(raw, "token=private"); err == nil {
			t.Fatalf("untrusted portal accepted: %s", raw)
		}
	}
	c.status.Confirmed = true
	if err := c.Refresh(context.Background()); err == nil || c.Snapshot().Confirmed {
		t.Fatal("failed refresh retained stale success")
	}
	if _, err := c.request(context.Background(), "GET", c.endpoint, nil, "", "application/json", false); err == nil {
		t.Fatal("oversized response accepted")
	}
	privateErr := errors.New("https://secret.example/?token=private")
	c.http.Transport = entitlementTransport(func(*http.Request) (*http.Response, error) { return nil, privateErr })
	if _, err := c.request(context.Background(), "GET", c.endpoint, nil, "", "application/json", false); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("sensitive network error exposed")
	}
	var zip bytes.Buffer
	writer := gzip.NewWriter(&zip)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
	_ = writer.Close()
	c.http.Transport = entitlementTransport(func(r *http.Request) (*http.Response, error) {
		response := entitlementResponse(r, zip.String())
		response.Header.Set("Content-Encoding", "gzip")
		return response, nil
	})
	if _, err := c.request(context.Background(), "GET", c.endpoint, nil, "", "application/json", false); err == nil {
		t.Fatal("oversized decompressed response accepted")
	}
}
