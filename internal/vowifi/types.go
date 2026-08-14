package vowifi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Phase is an evidence-backed VoWiFi lifecycle phase. A requested or enabled
// policy is deliberately not a phase: callers must inspect the concrete
// readiness fields before claiming that a carrier service is available.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseSIMReady    Phase = "sim_ready"
	PhaseAccessReady Phase = "access_ready"
	PhaseTunnelReady Phase = "tunnel_ready"
	PhaseIMSReady    Phase = "ims_ready"
	PhaseSMSReady    Phase = "sms_ready"
	PhaseFailed      Phase = "failed"
	PhaseStopping    Phase = "stopping"
)

// ResponderAUTHStatus records the evidence returned by the IKE implementation.
// Unknown and invalid are never accepted. Missing may only be accepted by an
// explicit compatibility policy and is then exposed as a high-risk audit.
type ResponderAUTHStatus string

const (
	ResponderAUTHUnknown  ResponderAUTHStatus = "unknown"
	ResponderAUTHVerified ResponderAUTHStatus = "verified"
	ResponderAUTHMissing  ResponderAUTHStatus = "missing"
	ResponderAUTHInvalid  ResponderAUTHStatus = "invalid"
)

const (
	AuditLevelHigh                          = "high"
	AuditCodeMissingResponderAUTH           = "missing_responder_auth_allowed"
	PhoneSourceAssociatedMSISDN             = "ims_associated_msisdn"
	PhoneSourcePAssociatedURI               = "ims_p_associated_uri"
	ProxyModeDirect               ProxyMode = "direct"
	ProxyModeSOCKS5               ProxyMode = "socks5"
)

var (
	ErrAlreadyEnabled            = errors.New("vowifi: already enabled")
	ErrNotRunning                = errors.New("vowifi: not running")
	ErrRetryRequiresFailure      = errors.New("vowifi: retry requires failed state")
	ErrInvalidIdentity           = errors.New("vowifi: invalid SIM identity")
	ErrTunnelNotEstablished      = errors.New("vowifi: tunnel is not established")
	ErrIMSNotRegistered          = errors.New("vowifi: IMS is not registered")
	ErrSMSNotReady               = errors.New("vowifi: SMS over IMS is not ready")
	ErrEAPAuthenticationRejected = errors.New("vowifi: EAP-AKA authentication rejected")
	ErrResponderAUTHRequired     = errors.New("vowifi: verified IKE responder AUTH is required")
	// ErrCleanupIncomplete marks a teardown that released its local IMS, tunnel,
	// and radio resources but hit a non-fatal network-side error (for example a
	// rejected SIP deregistration). Reconnect treats it as best-effort and
	// still rebuilds the runtime instead of wedging in the failed state.
	ErrCleanupIncomplete = errors.New("vowifi: cleanup incomplete")
)

// SecurityAudit is safe to expose through a status API. It contains no keying
// material, identities, proxy credentials, or raw IKE payloads.
type SecurityAudit struct {
	ResponderAUTH         ResponderAUTHStatus `json:"responder_auth"`
	CompatibilityOverride bool                `json:"compatibility_override"`
	HighRisk              bool                `json:"high_risk"`
	Level                 string              `json:"level,omitempty"`
	Code                  string              `json:"code,omitempty"`
	Message               string              `json:"message,omitempty"`
	IKEEncryption         string              `json:"ike_encryption,omitempty"`
	IKEIntegrity          string              `json:"ike_integrity,omitempty"`
	IKEDHGroup            string              `json:"ike_dh_group,omitempty"`
	ESPEncryption         string              `json:"esp_encryption,omitempty"`
	ESPIntegrity          string              `json:"esp_integrity,omitempty"`
}

// State is an immutable snapshot when returned by Orchestrator.State or a
// subscription. Enabled means desired policy; Active means a tunnel session
// exists. Neither is proof of IMS registration.
type State struct {
	DeviceID           string        `json:"device_id"`
	ICCID              string        `json:"iccid,omitempty"`
	IMSI               string        `json:"imsi,omitempty"`
	Phase              Phase         `json:"phase"`
	Enabled            bool          `json:"enabled"`
	Active             bool          `json:"active"`
	SIMReady           bool          `json:"sim_ready"`
	AccessReady        bool          `json:"access_ready"`
	TunnelReady        bool          `json:"tunnel_ready"`
	IMSReady           bool          `json:"ims_ready"`
	SMSReady           bool          `json:"sms_ready"`
	PureAirplanePolicy bool          `json:"pure_airplane_policy"`
	HomeMCC            string        `json:"home_mcc,omitempty"`
	HomeMNC            string        `json:"home_mnc,omitempty"`
	EPDG               string        `json:"epdg,omitempty"`
	ProxyMode          ProxyMode     `json:"proxy_mode,omitempty"`
	ProxyID            string        `json:"proxy_id,omitempty"`
	TunnelName         string        `json:"tunnel_name,omitempty"`
	DataplaneMode      string        `json:"dataplane_mode,omitempty"`
	IMSRegistration    string        `json:"ims_registration,omitempty"`
	PhoneNumber        string        `json:"phone_number,omitempty"`
	PhoneNumberSource  string        `json:"phone_number_source,omitempty"`
	LastErrorClass     string        `json:"last_error_class,omitempty"`
	LastError          string        `json:"last_error,omitempty"`
	LastReason         string        `json:"last_reason,omitempty"`
	Warnings           []string      `json:"warnings,omitempty"`
	CleanupErrors      []string      `json:"cleanup_errors,omitempty"`
	Security           SecurityAudit `json:"security"`
	Attempt            uint64        `json:"attempt"`
	Sequence           uint64        `json:"sequence"`
	StartedAt          *time.Time    `json:"started_at,omitempty"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

func (state State) clone() State {
	state.Warnings = append([]string(nil), state.Warnings...)
	state.CleanupErrors = append([]string(nil), state.CleanupErrors...)
	if state.StartedAt != nil {
		startedAt := *state.StartedAt
		state.StartedAt = &startedAt
	}
	return state
}

// SIMIdentity contains only information required by providers. It is never
// copied wholesale into State, which avoids accidentally exposing IMSI/ICCID.
// HomeMCC and HomeMNC must be supplied by the SIM reader; the orchestrator does
// not guess MNC length or a phone number from IMSI.
type SIMIdentity struct {
	ICCID           string
	IMSI            string
	IMEI            string
	HomeMCC         string
	HomeMNC         string
	HomeCountryCode string
	EPDG            string
	// SMSC is the TS-Service-Centre address used to build SMS-over-IMS
	// RP-DATA. It is optional during identity discovery, but IMS submission
	// requires it.
	SMSC string
}

func (identity SIMIdentity) validate() error {
	if strings.TrimSpace(identity.ICCID) == "" {
		return fmt.Errorf("%w: ICCID is empty", ErrInvalidIdentity)
	}
	if !isNDigits(identity.HomeMCC, 3, 3) {
		return fmt.Errorf("%w: home MCC must contain three digits", ErrInvalidIdentity)
	}
	if !isNDigits(identity.HomeMNC, 2, 3) {
		return fmt.Errorf("%w: home MNC must contain two or three digits", ErrInvalidIdentity)
	}
	return nil
}

// AKAEvidence proves that a usable USIM/ISIM AKA application was opened. It
// intentionally contains no secret material or authentication vectors.
type AKAEvidence struct {
	Ready       bool
	Application string
}

// AKAChallenge is the exact UMTS AKA input carried by EAP-AKA. Fixed-size
// fields prevent a provider from silently accepting truncated RAND or AUTN
// values.
type AKAChallenge struct {
	RAND [16]byte
	AUTN [16]byte
}

// AKAResult is either a successful USIM authentication vector (RES/CK/IK), or
// synchronization-failure evidence (AUTS). Implementations must never place
// CK, IK, RAND, AUTN, or AUTS in errors or logs.
type AKAResult struct {
	RES                    []byte
	CK                     []byte
	IK                     []byte
	AUTS                   []byte
	SynchronizationFailure bool
}

type RadioSnapshot struct {
	CellularDataEnabled bool
	OperatingMode       int
	PureAirplanePolicy  bool
}

type ProxyMode string

// ProxyRoute may carry credentials to TunnelProvider, but State only receives
// Mode and ID. Provider implementations must not include Password in errors.
type ProxyRoute struct {
	Mode     ProxyMode
	ID       string
	Address  string
	Username string
	Password string
}

type ProxyRequest struct {
	DeviceID    string
	ICCID       string
	HomeMCC     string
	HomeMNC     string
	CountryCode string
}

type TunnelSecurityPolicy struct {
	AllowMissingResponderAUTH bool
}

type TunnelRequest struct {
	DeviceID string
	Identity SIMIdentity
	EPDG     string
	Proxy    ProxyRoute
	AKA      AKAProvider
	Security TunnelSecurityPolicy
}

type TunnelEvidence struct {
	Established   bool
	Name          string
	DataplaneMode string
	LocalIPv4     string
	LocalIPv6     string
	PCSCF         []string
	ResponderAUTH ResponderAUTHStatus
	IKEEncryption string
	IKEIntegrity  string
	IKEDHGroup    string
	ESPEncryption string
	ESPIntegrity  string
}

type IMSRequest struct {
	DeviceID string
	Identity SIMIdentity
	Tunnel   TunnelSession
}

type IMSEvidence struct {
	Registered           bool
	RegistrationState    string
	AssociatedMSISDN     string
	PAssociatedURI       []string
	AssociatedIdentities []string
	RegisteredContact    string
	ServiceRoute         []string
	Transport            string
	LastSIPCode          int
	SecurityMode         string
	SecurityVerified     bool
}

type SMSEvidence struct {
	Ready bool
}

type SMSSubmitRequest struct {
	Recipient string
	Text      string
}

type SMSSubmitPart struct {
	Part             int       `json:"part"`
	Total            int       `json:"total"`
	Reference        int       `json:"reference"`
	SIPCode          int       `json:"sipCode"`
	Accepted         bool      `json:"accepted"`
	SubmittedAt      time.Time `json:"submittedAt"`
	SubmissionStatus string    `json:"submissionStatus"`
}

// SMSSubmitResult proves acceptance by the IMS SIP endpoint. It does not
// claim that the recipient read the message.
type SMSSubmitResult struct {
	To                string          `json:"to"`
	Encoding          string          `json:"encoding"`
	SubmittedAt       time.Time       `json:"submittedAt"`
	PartsTotal        int             `json:"partsTotal"`
	PartsAttempted    int             `json:"partsAttempted"`
	PartsAccepted     int             `json:"partsAccepted"`
	AllPartsAccepted  bool            `json:"allPartsAccepted"`
	ConcatReference   *int            `json:"concatReference,omitempty"`
	SubmissionStatus  string          `json:"submissionStatus"`
	DeliveryConfirmed bool            `json:"deliveryConfirmed"`
	PartResults       []SMSSubmitPart `json:"partResults"`
}

type PhoneRecord struct {
	ICCID     string
	Number    string
	Source    string
	UpdatedAt time.Time
}

// SIMIdentityReader reads live SIM identity and home PLMN information.
type SIMIdentityReader interface {
	ReadIdentity(context.Context, string) (SIMIdentity, error)
}

// SMSCenterReader optionally supplies the SIM-configured service-centre
// address needed for mobile-originated SMS over IMS.
type SMSCenterReader interface {
	ReadSMSCenter(context.Context, string) (string, error)
}

// AKAProvider validates AKA availability and is passed to TunnelProvider so
// the latter can answer EAP-AKA challenges without exporting SIM secrets.
type AKAProvider interface {
	CheckReady(context.Context, SIMIdentity) (AKAEvidence, error)
	Authenticate(context.Context, SIMIdentity, AKAChallenge) (AKAResult, error)
}

// PreferredAKAProvider optionally lets an AKA provider select a carrier-
// provisioned application such as ISIM. Providers that only expose USIM keep
// implementing AKAProvider unchanged.
type PreferredAKAProvider interface {
	AKAProvider
	AuthenticateWithPreference(context.Context, SIMIdentity, AKAChallenge, string) (AKAResult, error)
}

// RadioController owns the host/modem radio projection. EnterVoWiFiRFOff must
// not toggle the independent pure-airplane policy; Restore must return to the
// captured pre-transaction state.
type RadioController interface {
	Snapshot(context.Context, string) (RadioSnapshot, error)
	StopCellularData(context.Context, string) error
	EnterVoWiFiRFOff(context.Context, string) error
	Restore(context.Context, string, RadioSnapshot) error
}

// ProxyResolver maps the SIM home country/MCC to either a SOCKS5 route or
// direct transport.
type ProxyResolver interface {
	Resolve(context.Context, ProxyRequest) (ProxyRoute, error)
}

// TunnelProvider establishes SWu/IKEv2/IPsec. The Start context bounds setup;
// a returned session remains alive until Close is called.
type TunnelProvider interface {
	Start(context.Context, TunnelRequest) (TunnelSession, error)
}

type TunnelSession interface {
	Evidence() TunnelEvidence
	Close(context.Context) error
}

// RuntimeFailureNotifier is an optional long-lived session capability. A
// provider sends only terminal failures; normal Close must not send or close
// the channel. This lets the orchestrator revoke stale readiness evidence.
type RuntimeFailureNotifier interface {
	Failures() <-chan error
}

// IMSProvider registers IMS over an established tunnel. The Start context
// bounds setup; a returned session remains alive until Close is called.
type IMSProvider interface {
	Start(context.Context, IMSRequest) (IMSSession, error)
}

type IMSSession interface {
	Evidence() IMSEvidence
	EnableSMS(context.Context) (SMSEvidence, error)
	Close(context.Context) error
}

// SMSSender is an optional capability of a registered IMS session.
type SMSSender interface {
	SendSMS(context.Context, SMSSubmitRequest) (SMSSubmitResult, error)
}

// Call describes one IMS call and reports whether an RTP media stream is
// available to an authenticated extension.
type Call struct {
	ID         string     `json:"id"`
	Number     string     `json:"number"`
	Direction  string     `json:"direction"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	SIPCode    int        `json:"sip_code,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	MediaReady bool       `json:"media_ready,omitempty"`
	Codec      string     `json:"codec,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
}

// CallController is an optional capability of an IMS session. Media remains a
// separate optional interface so call signalling does not depend on a codec.
type CallController interface {
	Calls() []Call
	DialCall(context.Context, string) (Call, error)
	AnswerCall(context.Context, string) (Call, error)
	HangupCall(context.Context, string) error
}

// CallMedia is a narrow, codec-independent bridge between an IMS RTP stream
// and a trusted local extension. Samples are signed 16-bit mono PCM at 8 kHz.
type CallMedia interface {
	Codec() string
	ReadPCM(context.Context) ([]int16, error)
	WritePCM([]int16) error
}

// CallMediaController is optional so signalling-only IMS implementations stay
// compatible. Media is only exposed for a specific active call.
type CallMediaController interface {
	CallMedia(context.Context, string) (CallMedia, error)
}

// PhoneStore persists a number only after it was explicitly associated by IMS.
type PhoneStore interface {
	SaveAssociatedNumber(context.Context, PhoneRecord) error
}

type Dependencies struct {
	SIM    SIMIdentityReader
	AKA    AKAProvider
	Radio  RadioController
	Proxy  ProxyResolver
	Tunnel TunnelProvider
	IMS    IMSProvider
	Phones PhoneStore
}

type Options struct {
	DeviceID                  string
	AllowMissingResponderAUTH bool
	AllowIMSWithoutSMS        bool
	CleanupTimeout            time.Duration
}

func (options Options) validate() error {
	if strings.TrimSpace(options.DeviceID) == "" {
		return errors.New("vowifi: device ID is required")
	}
	if options.CleanupTimeout < 0 {
		return errors.New("vowifi: cleanup timeout must not be negative")
	}
	return nil
}

func (deps Dependencies) validate() error {
	switch {
	case deps.SIM == nil:
		return errors.New("vowifi: SIM identity reader is required")
	case deps.AKA == nil:
		return errors.New("vowifi: AKA provider is required")
	case deps.Radio == nil:
		return errors.New("vowifi: radio controller is required")
	case deps.Proxy == nil:
		return errors.New("vowifi: proxy resolver is required")
	case deps.Tunnel == nil:
		return errors.New("vowifi: tunnel provider is required")
	case deps.IMS == nil:
		return errors.New("vowifi: IMS provider is required")
	case deps.Phones == nil:
		return errors.New("vowifi: phone store is required")
	default:
		return nil
	}
}

type StageError struct {
	Stage Phase
	Err   error
}

func (err *StageError) Error() string {
	return fmt.Sprintf("vowifi %s: %v", err.Stage, err.Err)
}

func (err *StageError) Unwrap() error {
	return err.Err
}

func isNDigits(value string, minimum int, maximum int) bool {
	value = strings.TrimSpace(value)
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
