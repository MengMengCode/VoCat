package device

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"vocat/internal/netguard"
)

// es9pClient speaks SGP.22 ES9+ — JSON over HTTPS — to one SM-DP+. It is the
// network half of the LPA download flow: the host authenticates nothing itself
// (the eUICC does all certificate verification on-card); it only shuttles the
// base64 DER blobs between the SM-DP+ and the eUICC.
//
// The wire contract mirrors lpac's euicc/es9p.c: every request is a POST to
// https://<smdp>/gsma/rsp2/es9plus/<function> with a fixed header set, binary
// fields base64-encoded, and the reply envelope carries the outcome in
// header.functionExecutionStatus (with statusCodeData.message holding the
// human-readable failure, e.g. "The matchingID is not found").
type es9pClient struct {
	smdp     string
	endpoint *url.URL
	http     *http.Client
}

var smdpAddressPattern = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)

func newES9PClient(ctx context.Context, smdp string) (*es9pClient, error) {
	smdp = strings.TrimSpace(smdp)
	if !smdpAddressPattern.MatchString(smdp) {
		return nil, errors.New("esim: SM-DP+ address must be a hostname with an optional port")
	}
	candidate, err := url.Parse("https://" + smdp)
	if err != nil || candidate.Hostname() == "" || candidate.User != nil ||
		(candidate.Path != "" && candidate.Path != "/") || candidate.RawQuery != "" || candidate.Fragment != "" {
		return nil, errors.New("esim: SM-DP+ address must be a hostname with an optional port")
	}
	candidate.Path = ""
	validated, err := netguard.ValidatePublicURL(ctx, candidate.String(), true)
	if err != nil {
		return nil, fmt.Errorf("esim: unsafe SM-DP+ address: %w", err)
	}
	return &es9pClient{
		smdp:     validated.Host,
		endpoint: validated,
		http:     netguard.NewPublicHTTPClient(90*time.Second, true),
	}, nil
}

// es9pError is a failed ES9+ functionExecutionStatus. Message is the SM-DP+'s
// own explanation (surfaced verbatim, as the reference implementation does).
type es9pError struct {
	Function    string
	Status      string
	Message     string
	SubjectCode string
	ReasonCode  string
}

func (e *es9pError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if mapped := es9pErrorMessage(e.SubjectCode, e.ReasonCode); mapped != "" {
		return mapped
	}
	if e.Status != "" {
		return fmt.Sprintf("SM-DP+ %s failed (%s)", e.Function, e.Status)
	}
	return fmt.Sprintf("SM-DP+ %s failed", e.Function)
}

// es9pStatusCodeData mirrors header.functionExecutionStatus.statusCodeData.
type es9pStatusCodeData struct {
	ReasonCode        string `json:"reasonCode"`
	SubjectCode       string `json:"subjectCode"`
	SubjectIdentifier string `json:"subjectIdentifier"`
	Message           string `json:"message"`
}

// call POSTs one ES9+ function and returns the parsed top-level fields. Failure
// is decided the way lpac decides it: a non-success execution status, or a
// missing required output field, yields an es9pError carrying the SM-DP+ message.
func (c *es9pClient) call(ctx context.Context, function string, request map[string]string, requiredOut ...string) (map[string]json.RawMessage, error) {
	endpoint := *c.endpoint
	endpoint.Path = "/gsma/rsp2/es9plus/" + function
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "gsma-rsp-lpad")
	httpReq.Header.Set("X-Admin-Protocol", "gsma/rsp/v2.2.2")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("es9p %s: %w", function, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("es9p %s: read response: %w", function, err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("es9p %s: invalid JSON (HTTP %d): %w", function, resp.StatusCode, err)
	}

	var header struct {
		FunctionExecutionStatus struct {
			Status         string              `json:"status"`
			StatusCodeData *es9pStatusCodeData `json:"statusCodeData"`
		} `json:"functionExecutionStatus"`
	}
	if raw, ok := root["header"]; ok {
		_ = json.Unmarshal(raw, &header)
	}
	fes := header.FunctionExecutionStatus

	// A non-success execution status is an outright failure.
	switch fes.Status {
	case "", "Executed-Success", "Executed-WithWarning":
		// proceed
	default:
		return nil, es9pErrFromStatus(function, fes.Status, fes.StatusCodeData)
	}
	// Success means the expected output fields are present at the top level.
	for _, key := range requiredOut {
		if _, ok := root[key]; !ok {
			return nil, es9pErrFromStatus(function, fes.Status, fes.StatusCodeData)
		}
	}
	return root, nil
}

func es9pErrFromStatus(function, status string, scd *es9pStatusCodeData) error {
	err := &es9pError{Function: function, Status: status}
	if scd != nil {
		err.Message = scd.Message
		err.SubjectCode = scd.SubjectCode
		err.ReasonCode = scd.ReasonCode
	}
	return err
}

// es9pErrorMessage maps an SGP.22 (subjectCode, reasonCode) pair to a
// human-readable failure when the SM-DP+ omits statusCodeData.message. Table
// mirrors lpac's euicc/es9p_errors.c.
var es9pErrorTable = map[[2]string]string{
	{"8.1", "4.8"}:    "eUICC does not have sufficient space for this Profile",
	{"8.1", "6.1"}:    "eUICC signature is invalid or serverChallenge is invalid",
	{"8.1.1", "2.2"}:  "EID is missing in the context of this order",
	{"8.1.1", "3.1"}:  "a different EID is already associated with this ICCID",
	{"8.1.1", "3.8"}:  "EID doesn't match the expected value",
	{"8.1.2", "6.1"}:  "EUM Certificate is invalid",
	{"8.1.2", "6.3"}:  "EUM Certificate has expired",
	{"8.1.3", "6.1"}:  "eUICC Certificate is invalid",
	{"8.1.3", "6.3"}:  "eUICC Certificate has expired",
	{"8.2", "1.2"}:    "Profile has not yet been released",
	{"8.2", "3.7"}:    "BPP is not available for a new binding",
	{"8.2.5", "3.7"}:  "No more Profile available for the requested Profile Type",
	{"8.2.5", "4.3"}:  "No eligible Profile for this eUICC/Device",
	{"8.2.6", "3.1"}:  "a different MatchingID is associated with this ICCID",
	{"8.2.6", "3.3"}:  "Conflicting MatchingID value",
	{"8.2.6", "3.8"}:  "MatchingID (AC_Token or EventID) is refused",
	{"8.2.7", "2.2"}:  "Confirmation Code is missing",
	{"8.2.7", "3.8"}:  "Confirmation Code is refused",
	{"8.2.7", "6.4"}:  "maximum number of retries for the Confirmation Code exceeded",
	{"8.8.1", "3.8"}:  "Invalid SM-DP+ Address",
	{"8.8.4", "3.7"}:  "The SM-DP+ has no CERT.DPauth.ECDSA signed by one of the CI Public Key supported by the eUICC",
	{"8.8.5", "4.1"}:  "The Download order has expired",
	{"8.8.5", "6.4"}:  "maximum number of retries for the Profile download order exceeded",
	{"8.10.1", "3.9"}: "The RSP session identified by the TransactionID is unknown",
	{"8.11.1", "3.9"}: "Unknown CI Public Key. The CI used by the EUM Certificate is not a trusted root.",
}

func es9pErrorMessage(subjectCode, reasonCode string) string {
	return es9pErrorTable[[2]string{subjectCode, reasonCode}]
}

// es9pString extracts a plain string field.
func es9pString(root map[string]json.RawMessage, key string) (string, error) {
	raw, ok := root[key]
	if !ok {
		return "", fmt.Errorf("es9p: response missing %s", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("es9p: decode %s: %w", key, err)
	}
	return value, nil
}

// es9pB64 extracts and base64-decodes a binary field.
func es9pB64(root map[string]json.RawMessage, key string) ([]byte, error) {
	value, err := es9pString(root, key)
	if err != nil {
		return nil, err
	}
	return es9pBase64Decode(value)
}

func es9pBase64Decode(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func es9pBase64Encode(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}

// es9pInitiateResult carries the server's half of mutual authentication.
type es9pInitiateResult struct {
	TransactionID       string
	ServerSigned1       []byte
	ServerSignature1    []byte
	EuiccCiPKIDToBeUsed []byte
	ServerCertificate   []byte
}

func (c *es9pClient) initiateAuthentication(ctx context.Context, euiccChallenge, euiccInfo1 []byte) (*es9pInitiateResult, error) {
	root, err := c.call(ctx, "initiateAuthentication", map[string]string{
		"smdpAddress":    c.smdp,
		"euiccChallenge": es9pBase64Encode(euiccChallenge),
		"euiccInfo1":     es9pBase64Encode(euiccInfo1),
	}, "transactionId", "serverSigned1", "serverSignature1", "euiccCiPKIdToBeUsed", "serverCertificate")
	if err != nil {
		return nil, err
	}
	result := &es9pInitiateResult{}
	if result.TransactionID, err = es9pString(root, "transactionId"); err != nil {
		return nil, err
	}
	if result.ServerSigned1, err = es9pB64(root, "serverSigned1"); err != nil {
		return nil, err
	}
	if result.ServerSignature1, err = es9pB64(root, "serverSignature1"); err != nil {
		return nil, err
	}
	if result.EuiccCiPKIDToBeUsed, err = es9pB64(root, "euiccCiPKIdToBeUsed"); err != nil {
		return nil, err
	}
	if result.ServerCertificate, err = es9pB64(root, "serverCertificate"); err != nil {
		return nil, err
	}
	return result, nil
}

// es9pAuthenticateResult carries the profile metadata and the SM-DP+ download
// authorization needed for PrepareDownload.
type es9pAuthenticateResult struct {
	TransactionID   string
	ProfileMetadata []byte
	SmdpSigned2     []byte
	SmdpSignature2  []byte
	SmdpCertificate []byte
}

func (c *es9pClient) authenticateClient(ctx context.Context, transactionID string, authenticateServerResponse []byte) (*es9pAuthenticateResult, error) {
	root, err := c.call(ctx, "authenticateClient", map[string]string{
		"transactionId":              transactionID,
		"authenticateServerResponse": es9pBase64Encode(authenticateServerResponse),
	}, "profileMetadata", "smdpSigned2", "smdpSignature2", "smdpCertificate")
	if err != nil {
		return nil, err
	}
	result := &es9pAuthenticateResult{TransactionID: transactionID}
	if result.ProfileMetadata, err = es9pB64(root, "profileMetadata"); err != nil {
		return nil, err
	}
	if result.SmdpSigned2, err = es9pB64(root, "smdpSigned2"); err != nil {
		return nil, err
	}
	if result.SmdpSignature2, err = es9pB64(root, "smdpSignature2"); err != nil {
		return nil, err
	}
	if result.SmdpCertificate, err = es9pB64(root, "smdpCertificate"); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *es9pClient) getBoundProfilePackage(ctx context.Context, transactionID string, prepareDownloadResponse []byte) ([]byte, error) {
	root, err := c.call(ctx, "getBoundProfilePackage", map[string]string{
		"transactionId":           transactionID,
		"prepareDownloadResponse": es9pBase64Encode(prepareDownloadResponse),
	}, "boundProfilePackage")
	if err != nil {
		return nil, err
	}
	return es9pB64(root, "boundProfilePackage")
}

// handleNotification delivers a pending notification (a ProfileInstallationResult
// for the download case). It is best-effort: the profile is already installed, so
// a notification failure is reported by the caller as a warning, not a failure.
func (c *es9pClient) handleNotification(ctx context.Context, pendingNotification []byte) error {
	endpoint := *c.endpoint
	endpoint.Path = "/gsma/rsp2/es9plus/handleNotification"
	body, err := json.Marshal(map[string]string{
		"pendingNotification": es9pBase64Encode(pendingNotification),
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "gsma-rsp-lpad")
	request.Header.Set("X-Admin-Protocol", "gsma/rsp/v2.2.2")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("es9p handleNotification: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	// SGP.22 defines HandleNotification as a notification-handler function:
	// success is an empty HTTP 204 response, not the JSON envelope returned by
	// ordinary ES9+ request-response functions.
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("es9p handleNotification: receiver returned HTTP %d", response.StatusCode)
	}
	return nil
}

// cancelSession aborts an in-flight download so the SM-DP+ releases the
// transaction. Best-effort cleanup on error/abort paths.
func (c *es9pClient) cancelSession(ctx context.Context, transactionID string, cancelSessionResponse []byte) error {
	_, err := c.call(ctx, "cancelSession", map[string]string{
		"transactionId":         transactionID,
		"cancelSessionResponse": es9pBase64Encode(cancelSessionResponse),
	})
	return err
}
