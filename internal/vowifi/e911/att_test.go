package e911

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"vocat/internal/vowifi"
)

// Synthetic EAP-AKA vector, independently generated with the FIPS 186-2 PRF.
// All identities, RAND/AUTN and keys below are test data, not subscriber data.
const testChallenge = "AQcARBcBAAABBQAABAQEBAQEBAQEBAQEBAQEBAIFAAAFBQUFBQUFBQUFBQUFBQUFCwUAAL5Z5P76dSffjn2sC0slUQU="
const testAnswer = "AgcAKBcBAAADAwBAAwMDAwMDAwMLBQAAbtoU2Iy7eOIq/rCoYxqPTA=="

type testAKA struct{ calls int }

func (*testAKA) CheckReady(context.Context, vowifi.SIMIdentity) (vowifi.AKAEvidence, error) {
	return vowifi.AKAEvidence{Ready: true}, nil
}
func (a *testAKA) Authenticate(_ context.Context, _ vowifi.SIMIdentity, challenge vowifi.AKAChallenge) (vowifi.AKAResult, error) {
	a.calls++
	return vowifi.AKAResult{IK: bytes.Repeat([]byte{1}, 16), CK: bytes.Repeat([]byte{2}, 16), RES: bytes.Repeat([]byte{3}, 8)}, nil
}

type carrierTranscript struct {
	t                                 *testing.T
	authenticated, activated, created bool
	pending, mismatch, rejectWrite    bool
	webURL                            string
	verified, writes, webRequests     int
}

func (f *carrierTranscript) RoundTrip(r *http.Request) (*http.Response, error) {
	respond := func(status int, body any) (*http.Response, error) {
		var data []byte
		if text, ok := body.(string); ok {
			data = []byte(text)
		} else {
			data, _ = json.Marshal(body)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Graph": {"test-graph"}}, Body: io.NopCloser(bytes.NewReader(data)), Request: r}, nil
	}
	if r.URL.String() == nsdsURL {
		reader, err := gzip.NewReader(r.Body)
		if err != nil {
			f.t.Fatal(err)
		}
		defer reader.Close()
		var requests []map[string]any
		if err := json.NewDecoder(reader).Decode(&requests); err != nil {
			f.t.Fatal(err)
		}
		if !f.authenticated {
			if requests[0]["aka-challenge-rsp"] == nil {
				return respond(200, []any{map[string]any{"message-id": 1, "response-code": 1003, "aka-challenge": testChallenge}})
			}
			if requests[0]["aka-challenge-rsp"] != testAnswer {
				f.t.Fatalf("incorrect EAP-AKA response")
			}
			f.authenticated = true
			return respond(200, []any{map[string]any{"message-id": 1, "response-code": 1000, "aka-token": "private-test-token"}})
		}
		if requests[0]["aka-token"] != "private-test-token" {
			f.t.Fatal("AKA token missing")
		}
		message := map[string]any{"message-id": 2, "response-code": 1000}
		switch requests[1]["method"] {
		case "getMSISDN":
			message["service-fingerprint"] = "private-test-fingerprint"
		case "manageLocationAndTC":
			if requests[1]["service-fingerprint"] != "private-test-fingerprint" {
				f.t.Fatal("fingerprint missing")
			}
			u := f.webURL
			if u == "" {
				u = webOrigin + "/softphone/primary/geni"
			}
			message["server-url"] = u
			message["server-data"] = "private-test-bootstrap"
			message["location-status"] = f.created
			message["tc-status"] = f.activated
		case "serviceEntitlementStatus":
			code := 1048
			if f.activated {
				code = 1000
			}
			message["service-entitlement"] = []any{map[string]any{"service-name": "vowifi", "entitlement-status": code}}
		default:
			f.t.Fatalf("unexpected NSDS method %v", requests[1]["method"])
		}
		return respond(200, []any{map[string]any{"message-id": 1, "response-code": 1000}, message})
	}
	f.webRequests++
	if r.URL.Path == "/softphone/primary/geni" {
		if r.Method != "POST" {
			f.t.Fatal("bootstrap must POST the carrier token")
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("token") != "private-test-bootstrap" {
			f.t.Fatal("missing bootstrap token")
		}
		return respond(200, `<script>var SoftPhoneToken = "private\-test-web-token";</script>`)
	}
	if r.Header.Get("Token") != "private-test-web-token" || (r.URL.Path != "/sfservice/v1/appinfo/geni" && r.Header.Get("Graph") != "test-graph") {
		f.t.Fatal("missing carrier session headers")
	}
	switch r.URL.Path {
	case "/sfservice/v1/appinfo/geni":
		return respond(200, map[string]any{})
	case "/sfservice/v1/eligibility/reseller/geni":
		return respond(200, map[string]any{"activate": !f.activated})
	case "/sfservice/v1/information/important/reseller/geni":
		return respond(200, `<p>Carrier Wi-Fi Calling terms</p><script>doNotRender()</script>`)
	case "/sfservice/v1/address/e911/verify/reseller/geni":
		f.verified++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["zipCode"] != "97403" {
			f.t.Fatal("address ZIP missing")
		}
		return respond(200, map[string]any{"addrOut": []any{map[string]any{"address1": "123 MAIN ST", "address2": "APT 4", "city": "EUGENE", "state": "OR", "zipCode": "97403", "csiAddress": true, "requestMatch": true}}})
	case "/sfservice/v1/device/primary/activate/reseller/geni":
		if r.Method != "PUT" {
			f.t.Fatal("wrong activation method")
		}
		f.activated = true
		return respond(200, "")
	case "/sfservice/v1/address/e911/reseller/geni":
		if r.Method == http.MethodPut {
			f.writes++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["address1"] != "123 MAIN ST" || body["address2"] != "APT 4" || body["csiAddress"] != true {
				f.t.Fatal("unvalidated address was submitted")
			}
			if f.rejectWrite {
				return respond(400, map[string]any{"id": "CPS0042", "secret": "private-carrier-error"})
			}
			f.created = true
			return respond(201, map[string]any{"e911Address": map[string]any{"status": "Validated"}})
		}
		status, street := "Validated", "123 MAIN ST"
		if f.pending {
			status = "Pending"
		}
		if f.mismatch {
			street = "456 OTHER ST"
		}
		return respond(200, map[string]any{"addressId": "private-test-address-id", "status": status, "address": map[string]any{"address1": street, "address2": "APT 4", "city": "EUGENE", "state": "OR", "zipCode": "97403"}})
	default:
		f.t.Fatalf("unexpected carrier path %s", r.URL.Path)
	}
	return nil, nil
}

func transcriptClient(t *testing.T, f *carrierTranscript) (*ATTClient, *testAKA) {
	t.Helper()
	f.t = t
	aka := &testAKA{}
	c, err := NewATTClient(vowifi.SIMIdentity{IMSI: "310280000000001", ICCID: "test-card", IMEI: "123456789012345", HomeMCC: "310", HomeMNC: "280"}, aka, &http.Client{Transport: f})
	if err != nil {
		t.Fatal(err)
	}
	return c, aka
}

func TestATTProvisioningUsesSIMAndRequiresCarrierReadback(t *testing.T) {
	for _, tt := range []struct {
		name              string
		pending, mismatch bool
		confirmed         bool
	}{{"validated", false, false, true}, {"write accepted but validation pending", true, false, false}, {"readback address differs", false, true, false}} {
		t.Run(tt.name, func(t *testing.T) {
			f := &carrierTranscript{pending: tt.pending, mismatch: tt.mismatch}
			c, aka := transcriptClient(t, f)
			ctx := context.Background()
			if err := c.Start(ctx); err != nil {
				t.Fatal(err)
			}
			if aka.calls != 1 || c.Status.Confirmed || strings.Contains(c.Terms, "doNotRender") {
				t.Fatalf("bad initial state %#v", c.Status)
			}
			if _, err := c.Submit(ctx, 0, true); err == nil {
				t.Fatal("submit accepted without validation")
			}
			candidates, err := c.Verify(ctx, Address{Street: "123 Main St", Unit: "Apt 4", City: "Eugene", State: "OR", ZIP: "97403"})
			if err != nil || len(candidates) != 1 {
				t.Fatalf("verify: %v", err)
			}
			if _, err := c.Submit(ctx, 0, false); err == nil {
				t.Fatal("submit accepted without terms")
			}
			if f.activated || f.writes != 0 {
				t.Fatal("carrier was changed before consent")
			}
			status, err := c.Submit(ctx, 0, true)
			if err != nil {
				t.Fatal(err)
			}
			if status.Confirmed != tt.confirmed || f.writes != 1 {
				t.Fatalf("result=%#v writes=%d", status, f.writes)
			}
			if err := c.Refresh(ctx); err != nil || c.Status.Confirmed != tt.confirmed {
				t.Fatalf("refresh lost submitted-address confirmation: %#v %v", c.Status, err)
			}
			body, _ := json.Marshal(status)
			if strings.Contains(string(body), "private-") {
				t.Fatal("carrier credential exposed")
			}
		})
	}
}

func TestATTRejectsUntrustedPortalAndSanitizesCarrierErrors(t *testing.T) {
	f := &carrierTranscript{webURL: "https://attdashboard.wireless.att.com.evil.example/softphone/primary/geni"}
	c, _ := transcriptClient(t, f)
	if err := c.Start(context.Background()); err == nil || f.webRequests != 0 {
		t.Fatal("untrusted portal received carrier credentials")
	}
	f = &carrierTranscript{rejectWrite: true}
	c, _ = transcriptClient(t, f)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(ctx, Address{Street: "123 Main St", City: "Eugene", State: "OR", ZIP: "97403"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.Submit(ctx, 0, true)
	if err == nil || strings.Contains(err.Error(), "private-") || !strings.Contains(err.Error(), "CPS0042") {
		t.Fatalf("unsafe error: %v", err)
	}
}
