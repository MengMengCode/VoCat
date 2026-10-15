package e911

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"vocat/internal/vowifi"
	"vocat/internal/vowifi/ike"
)

// EntitlementClient implements TS.43 EAP relay and the MSP v2 SIM-authenticated
// websheet bootstrap. This is protocol support; it is not proof that a given
// subscription or modem IMEI is accepted by its carrier.
type EntitlementClient struct {
	http        *http.Client
	identity    vowifi.SIMIdentity
	eap         *ike.EAPAKAClient
	carrier     Carrier
	endpoint    string
	portalRoots []string
	portal      *Websheet
	status      Status
	token       string
	deviceID    string
	accountID   string
}

func newEntitlementAKA(identity vowifi.SIMIdentity, aka vowifi.AKAProvider) (*ike.EAPAKAClient, error) {
	return ike.NewEAPAKAClient(identity, aka)
}

func NewEntitlementClient(identity vowifi.SIMIdentity, aka vowifi.AKAProvider, carrier Carrier, client *http.Client) (*EntitlementClient, error) {
	if len(identity.IMEI) != 15 || strings.Trim(identity.IMEI, "0123456789") != "" {
		return nil, errors.New("the carrier requires the module's valid 15-digit IMEI")
	}
	eap, err := newEntitlementAKA(identity, aka)
	if err != nil {
		return nil, err
	}
	endpoint := ""
	roots := []string{}
	switch carrier.Network {
	case "tmobile":
		endpoint = "https://eas3.msg.t-mobile.com"
		roots = []string{"t-mobile.com", "metrobyt-mobile.com"}
	case "verizon":
		endpoint = "https://spc.myvzw.com/ium-RoadrunnerInterface/servlet/IUMServlet"
		roots = []string{"vzw.com", "myvzw.com", "verizon.com", "verizonwireless.com"}
	default:
		if len(identity.HomeMCC) != 3 || len(identity.HomeMNC) < 2 || len(identity.HomeMNC) > 3 || strings.Trim(identity.HomeMCC+identity.HomeMNC, "0123456789") != "" {
			return nil, errors.New("the SIM must supply a valid home MCC and MNC for entitlement discovery")
		}
		mnc := strings.Repeat("0", 3-len(identity.HomeMNC)) + identity.HomeMNC
		host := fmt.Sprintf("aes.mnc%s.mcc%s.pub.3gppnetwork.org", mnc, identity.HomeMCC)
		endpoint, roots = "https://"+host, []string{host}
		// Regional carriers using the standard discovery host can return a
		// service websheet on their own domain.
		switch identity.HomeMCC + identity.HomeMNC {
		case "311220", "311221":
			roots = append(roots, "uscellular.com")
		case "311230":
			roots = append(roots, "cspire.com")
		case "310600":
			roots = append(roots, "cellcom.com")
		}
	}
	// An MVNO may use its own service portal even when authentication is
	// delegated to its host. Only built-in account domains are eligible.
	if u, err := url.Parse(carrier.PortalURL); err == nil && u.Scheme == "https" && u.Hostname() != "" {
		roots = append(roots, strings.TrimPrefix(u.Hostname(), "www."))
	}
	if client == nil {
		jar, _ := cookiejar.New(nil)
		client = &http.Client{Timeout: 20 * time.Second, Jar: jar}
	} else {
		copy := *client
		client = &copy
		if client.Jar == nil {
			client.Jar, _ = cookiejar.New(nil)
		}
	}
	server, _ := url.Parse(endpoint)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" || req.URL.Host != server.Host || req.URL.User != nil {
			return errors.New("carrier redirected outside its entitlement service")
		}
		return nil
	}
	var account [16]byte
	if _, err := rand.Read(account[:]); err != nil {
		return nil, errors.New("could not create carrier device account identity")
	}
	account[6], account[8] = (account[6]&0x0f)|0x40, (account[8]&0x3f)|0x80
	encoded := strings.ToUpper(hex.EncodeToString(account[:]))
	accountID := encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
	return &EntitlementClient{http: client, identity: identity, eap: eap, carrier: carrier, endpoint: endpoint, portalRoots: roots, deviceID: identity.IMEI, accountID: accountID}, nil
}

func (c *EntitlementClient) Start(ctx context.Context) error { return c.Refresh(ctx) }
func (c *EntitlementClient) Snapshot() Status                { return c.status }
func (c *EntitlementClient) TermsText() string               { return "" }
func (c *EntitlementClient) Websheet() *Websheet             { return c.portal }
func (c *EntitlementClient) BindAKA(aka vowifi.AKAProvider) error {
	var err error
	c.eap, err = newEntitlementAKA(c.identity, aka)
	return err
}

func (c *EntitlementClient) Refresh(ctx context.Context) error {
	// Revoke stale success and portal handoffs before every query, including
	// network failures. A previous successful query must not mask a failure.
	c.status, c.portal = Status{}, nil
	if c.carrier.Method == "msp_websheet" {
		return c.refreshMSP(ctx)
	}
	return c.refreshTS43(ctx)
}

func (c *EntitlementClient) Verify(context.Context, Address) ([]Candidate, error) {
	return nil, errors.New("enter and validate the address in the carrier registration page")
}

func (c *EntitlementClient) Submit(context.Context, int, bool) (Status, error) {
	return c.status, errors.New("submit the address and carrier terms in the carrier registration page, then refresh status")
}

func (c *EntitlementClient) setWebsheet(rawURL, data string) error {
	if rawURL == "" {
		return nil
	}
	if !trustedWebsheet(rawURL, c.portalRoots) || len(data) > 65536 {
		return errors.New("carrier supplied an unrecognized emergency address portal")
	}
	fields, err := url.ParseQuery(data)
	if err != nil || len(fields) > 64 {
		return errors.New("carrier supplied unsupported emergency address form data")
	}
	c.portal = &Websheet{URL: rawURL, Fields: fields}
	return nil
}

// request deliberately excludes carrier response bodies, identity-bearing URLs,
// and network errors from errors/logs. Entitlement responses are size limited
// after decompression, and redirects cannot carry credentials to other hosts.
func (c *EntitlementClient) request(ctx context.Context, method, endpoint string, body []byte, contentType, accept string, compressed bool) ([]byte, error) {
	if compressed {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write(body)
		_ = writer.Close()
		body = buffer.Bytes()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not prepare carrier entitlement request")
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "PRD-TS43 VoCat/Module IMS-Entitlement/1.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if compressed {
		req.Header.Set("Content-Encoding", "gzip")
		req.Header.Set("X-Protocol-Version", "2")
		req.Header.Set("X-Country-Iso-Code", "us")
		req.Header.Set("User-Agent", "Entitlement/2 (VoCat) Carrier Settings/1.0")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, errors.New("could not reach the carrier entitlement service; retry or use the carrier account")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: response.StatusCode}
	}
	var reader io.Reader = response.Body
	if strings.EqualFold(response.Header.Get("Content-Encoding"), "gzip") {
		unzip, err := gzip.NewReader(reader)
		if err != nil {
			return nil, errors.New("invalid compressed carrier entitlement response")
		}
		defer unzip.Close()
		reader = unzip
	}
	result, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(result) > 1<<20 {
		return nil, errors.New("carrier entitlement response exceeds the size limit or could not be read")
	}
	return result, nil
}

func encodeJSON(value any) []byte {
	result, _ := json.Marshal(value)
	return result
}
