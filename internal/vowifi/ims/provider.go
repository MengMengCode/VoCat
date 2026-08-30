package ims

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vocat/internal/vowifi"
)

const (
	defaultSIPPort            = 5060
	defaultSIPUserAgent       = "vocat/1"
	imsUserAgentOverrideEnv   = "VOCAT_IMS_DEFAULT_USER_AGENT"
	defaultRegistrationExpiry = 3600 * time.Second
	defaultTransactionTimeout = 12 * time.Second
	defaultRegistrationRetry  = 60 * time.Second
	maxRegistrationRetry      = 5 * time.Minute
	registrationSafetyMargin  = 5 * time.Second
	// Temporary observation setting; restore the expiry-based schedule after
	// the non-outbound flow behavior has been verified.
	temporaryRegistrationRefreshInterval = 20 * time.Minute
	maxAuthenticationChallenges          = 3
	defaultPANIWLANNode                  = "ffffffffffff"
	sipFlowRecoveryBase                  = 30 * time.Second
	sipFlowRecoveryMax                   = 30 * time.Minute
)

var (
	ErrRegistrationRejected      = errors.New("ims: SIP registration was rejected")
	ErrSessionClosed             = errors.New("ims: session is closed")
	ErrRegistrationExpired       = errors.New("ims: registration has expired")
	ErrSMSCapabilityNotConfirmed = errors.New("ims: registrar did not confirm the +g.3gpp.smsip contact")
	errSIPConnectionUnavailable  = errors.New("ims: SIP connection is unavailable")
)

// Config defines only deployment-specific SIP details. If PCSCF or
// LocalAddress is empty, Provider uses the corresponding value proven by the
// TunnelSession. The default transport is TCP and the default port is 5060.
type Config struct {
	PCSCF           string
	LocalAddress    string
	Transport       string
	TransportByPLMN map[string]string
	// AutoTransportFallback tries the alternate TCP/UDP transport only when
	// the initial P-CSCF attempt produced no SIP response at all. A challenge
	// or rejection is authoritative and is never retried as another transport.
	AutoTransportFallback bool
	Port                  int
	RegistrationExpiry    time.Duration
	TransactionTimeout    time.Duration
	PrivateIdentity       string
	PublicIdentity        string
	UserAgent             string
	SecurityMode          SecurityMode
	IPSecInstaller        IPSecSAInstaller
	ProtectedClientPort   int
	ProtectedServerPort   int
	// SMSCenter is an operator-provided fallback when the SIM leaves EF_SMSP
	// and AT+CSCA empty. It must be an international or national digit string.
	SMSCenter string
	// SMSCenterByPLMN provides narrow carrier fallbacks without applying one
	// operator's service-centre address to every SIM.
	SMSCenterByPLMN map[string]string
	// OnSMS is invoked after a valid inbound RP-DATA/SMS-DELIVER has been
	// decoded. Returning an error causes an RP-ERROR delivery report.
	OnSMS func(context.Context, ReceivedSMS) error
	// OnSMSStatus is invoked for an SMS-STATUS-REPORT received after a
	// submission that requested a delivery report.
	OnSMSStatus func(context.Context, ReceivedSMSStatus) error
	// OnUSSD is invoked for a network-originated USSD MESSAGE received over
	// IMS (3GPP TS 24.390). Returning an error is logged but does not affect
	// the 200 OK already sent, because USSI has no RP-ACK transport.
	OnUSSD func(context.Context, ReceivedUSSD) error
	// OnIncomingCall is invoked when an incoming voice call (INVITE) is received over IMS.
	OnIncomingCall func(context.Context, ReceivedCall) error
	// Logger receives structured IMS runtime diagnostics. Inbound SMS logs do
	// not include message text or raw protocol payloads.
	Logger *slog.Logger
}

// ReceivedCall is an incoming voice call event delivered over IMS.
type ReceivedCall struct {
	DeviceID  string
	IMSI      string
	CallID    string
	Caller    string
	Called    string
	Timestamp time.Time
}

// Provider implements vowifi.IMSProvider using a small RFC 3261 REGISTER
// transaction and 3GPP AKAv1-MD5 authentication. It has no SIP stack or
// runtime dependency outside the Go standard library.
type Provider struct {
	aka            vowifi.AKAProvider
	config         Config
	installer      IPSecSAInstaller
	transportMu    sync.RWMutex
	transportCache map[string]string
}

func NewProvider(aka vowifi.AKAProvider, config Config) (*Provider, error) {
	if aka == nil {
		return nil, errors.New("ims: AKA provider is required")
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	installer := normalized.IPSecInstaller
	if installer == nil {
		installer = defaultIPSecInstaller()
	}
	return &Provider{
		aka: aka, config: normalized, installer: installer,
		transportCache: make(map[string]string),
	}, nil
}

func normalizeConfig(config Config) (Config, error) {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Port == 0 {
		config.Port = defaultSIPPort
	}
	if config.Port < 1 || config.Port > 65535 {
		return Config{}, errors.New("ims: SIP port is out of range")
	}
	if config.RegistrationExpiry == 0 {
		config.RegistrationExpiry = defaultRegistrationExpiry
	}
	if config.RegistrationExpiry < time.Minute || config.RegistrationExpiry > 24*time.Hour {
		return Config{}, errors.New("ims: registration expiry must be between one minute and 24 hours")
	}
	if config.TransactionTimeout == 0 {
		config.TransactionTimeout = defaultTransactionTimeout
	}
	if config.TransactionTimeout < time.Second || config.TransactionTimeout > time.Minute {
		return Config{}, errors.New("ims: transaction timeout must be between one second and one minute")
	}
	config.Transport = strings.ToLower(strings.TrimSpace(config.Transport))
	if config.Transport != "" && config.Transport != "udp" && config.Transport != "tcp" {
		return Config{}, fmt.Errorf("ims: unsupported SIP transport %q", config.Transport)
	}
	transportByPLMN := make(map[string]string, len(config.TransportByPLMN))
	for plmn, transport := range config.TransportByPLMN {
		plmn = strings.TrimSpace(plmn)
		transport = strings.ToLower(strings.TrimSpace(transport))
		if !digitsBetween(plmn, 5, 6) {
			return Config{}, fmt.Errorf("ims: invalid transport override PLMN %q", plmn)
		}
		if transport != "udp" && transport != "tcp" {
			return Config{}, fmt.Errorf("ims: unsupported SIP transport %q for PLMN %s", transport, plmn)
		}
		transportByPLMN[plmn] = transport
	}
	config.TransportByPLMN = transportByPLMN
	smsCenterByPLMN := make(map[string]string, len(config.SMSCenterByPLMN))
	for plmn, smsCenter := range config.SMSCenterByPLMN {
		plmn = strings.TrimSpace(plmn)
		smsCenter = strings.TrimSpace(smsCenter)
		if !digitsBetween(plmn, 5, 6) {
			return Config{}, fmt.Errorf("ims: invalid SMS service-centre PLMN %q", plmn)
		}
		if !validSMSCenter(smsCenter) {
			return Config{}, fmt.Errorf("ims: invalid SMS service-centre address for PLMN %s", plmn)
		}
		smsCenterByPLMN[plmn] = smsCenter
	}
	config.SMSCenterByPLMN = smsCenterByPLMN
	if config.SecurityMode == "" {
		config.SecurityMode = SecurityRequired
	}
	switch config.SecurityMode {
	case SecurityRequired, SecurityOptional, SecurityDisabled:
	default:
		return Config{}, fmt.Errorf("ims: unsupported security mode %q", config.SecurityMode)
	}
	if config.ProtectedClientPort != 0 && !validProtectedPort(config.ProtectedClientPort) {
		return Config{}, errors.New("ims: protected client port is invalid")
	}
	if config.ProtectedServerPort != 0 && !validProtectedPort(config.ProtectedServerPort) {
		return Config{}, errors.New("ims: protected server port is invalid")
	}
	if config.ProtectedClientPort != 0 &&
		config.ProtectedClientPort == config.ProtectedServerPort {
		return Config{}, errors.New("ims: protected client and server ports must differ")
	}
	for name, value := range map[string]string{
		"PCSCF": config.PCSCF, "local address": config.LocalAddress,
		"private identity": config.PrivateIdentity, "public identity": config.PublicIdentity,
		"user agent": config.UserAgent,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return Config{}, fmt.Errorf("ims: %s contains a line break", name)
		}
	}
	config.PCSCF = strings.TrimSpace(config.PCSCF)
	config.LocalAddress = strings.TrimSpace(config.LocalAddress)
	config.PrivateIdentity = strings.TrimSpace(config.PrivateIdentity)
	config.PublicIdentity = strings.TrimSpace(config.PublicIdentity)
	config.UserAgent = strings.TrimSpace(config.UserAgent)
	config.SMSCenter = strings.TrimSpace(config.SMSCenter)
	if config.SMSCenter != "" && !validSMSCenter(config.SMSCenter) {
		return Config{}, errors.New("ims: configured SMS service-centre address is invalid")
	}
	return config, nil
}

func validSMSCenter(value string) bool {
	digits := strings.TrimPrefix(strings.TrimSpace(value), "+")
	return digitsBetween(digits, 3, 20)
}

func (provider *Provider) Start(ctx context.Context, request vowifi.IMSRequest) (vowifi.IMSSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Tunnel == nil {
		return nil, errors.New("ims: tunnel session is required")
	}
	tunnel := request.Tunnel.Evidence()
	if !tunnel.Established {
		return nil, vowifi.ErrTunnelNotEstablished
	}
	identities, err := deriveIdentities(request.Identity, provider.config)
	if err != nil {
		return nil, err
	}
	var pcscfCandidates []string
	if provider.config.PCSCF != "" {
		pcscfCandidates = []string{provider.config.PCSCF}
	} else {
		for _, candidate := range tunnel.PCSCF {
			candidate = strings.TrimSpace(candidate)
			if candidate != "" {
				pcscfCandidates = append(pcscfCandidates, candidate)
			}
		}
	}
	if len(pcscfCandidates) == 0 {
		return nil, errors.New("ims: tunnel did not provide a P-CSCF")
	}

	var lastErr error
	for pcscfIndex, pcscf := range pcscfCandidates {
		endpoint, transportHint, err := parsePCSCF(pcscf, provider.config.Port)
		if err != nil {
			lastErr = err
			continue
		}
		if provider.config.PCSCF != "" && !pcscfProvenByTunnel(endpoint, tunnel.PCSCF, provider.config.Port) {
			return nil, errors.New("ims: configured P-CSCF is not proven by the SWu tunnel")
		}
		transport := provider.transportForEndpoint(request.Identity, endpoint, transportHint)
		localAddress := provider.config.LocalAddress
		if localAddress == "" {
			if endpointIP := net.ParseIP(endpoint.host); endpointIP != nil && endpointIP.To4() == nil {
				localAddress = tunnel.LocalIPv6
			} else {
				localAddress = tunnel.LocalIPv4
				if strings.TrimSpace(localAddress) == "" {
					localAddress = tunnel.LocalIPv6
				}
			}
		}
		localAddress = strings.TrimSpace(strings.Split(localAddress, "/")[0])
		if localAddress == "" {
			return nil, errors.New("ims: tunnel did not provide a local address")
		}
		if !localAddressProvenByTunnel(localAddress, tunnel) {
			return nil, errors.New("ims: configured local address is not assigned by the SWu tunnel")
		}

		transports := []string{transport}
		if provider.config.AutoTransportFallback {
			alternate := "udp"
			if transport == "udp" {
				alternate = "tcp"
			}
			transports = append(transports, alternate)
		}
		for attempt, candidate := range transports {
			connection, dialErr := dialSIP(ctx, candidate, localAddress, 0, endpoint.address())
			if dialErr != nil {
				lastErr = fmt.Errorf("ims: connect to P-CSCF over %s: %w", candidate, dialErr)
				if attempt+1 < len(transports) && ctx.Err() == nil {
					provider.logTransportFallback(request.Identity, candidate, transports[attempt+1], lastErr)
					continue
				}
				break
			}
			session, sessionErr := newSession(provider, request, identities, endpoint, candidate, connection)
			if sessionErr != nil {
				_ = connection.Close()
				lastErr = sessionErr
				break
			}
			establishErr := session.establish(ctx)
			if establishErr == nil {
				provider.rememberTransport(request.Identity, endpoint, candidate)
				if attempt > 0 || pcscfIndex > 0 {
					provider.config.Logger.Info("IMS automatic transport fallback succeeded",
						"carrier_profile", vowifi.ResolveCarrierProfile(request.Identity).ID,
						"transport", candidate)
				}
				return session, nil
			}
			sipResponseObserved := session.evidence.LastSIPCode != 0
			session.abort()
			lastErr = establishErr
			if sipResponseObserved || attempt+1 >= len(transports) || ctx.Err() != nil {
				break
			}
			provider.logTransportFallback(request.Identity, candidate, transports[attempt+1], establishErr)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func transportForIdentity(config Config, identity vowifi.SIMIdentity) string {
	if transport, selected := carrierTransportForIdentity(config, identity); selected {
		return transport
	}
	return config.Transport
}

func carrierTransportForIdentity(config Config, identity vowifi.SIMIdentity) (string, bool) {
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimSpace(identity.HomeMNC)
	if transport := config.TransportByPLMN[mcc+mnc]; transport != "" {
		return transport, true
	}
	if transport := vowifi.ResolveCarrierProfile(identity).IMSTransport; transport != "" {
		return transport, true
	}
	return "", false
}

func transportCacheKey(identity vowifi.SIMIdentity, endpoint pcscfEndpoint) string {
	identityKey := ""
	if iccid := strings.TrimSpace(identity.ICCID); iccid != "" {
		identityKey = "iccid:" + iccid
	} else {
		identityKey = "plmn:" + strings.TrimSpace(identity.HomeMCC) + "/" + strings.TrimSpace(identity.HomeMNC)
	}
	profileID := strings.TrimSpace(vowifi.ResolveCarrierProfile(identity).ID)
	return identityKey + "|profile:" + profileID + "|pcscf:" + strings.ToLower(endpoint.address())
}

func (provider *Provider) cachedTransport(identity vowifi.SIMIdentity, endpoint pcscfEndpoint) string {
	provider.transportMu.RLock()
	transport := provider.transportCache[transportCacheKey(identity, endpoint)]
	provider.transportMu.RUnlock()
	return transport
}

func (provider *Provider) rememberTransport(identity vowifi.SIMIdentity, endpoint pcscfEndpoint, transport string) {
	provider.transportMu.Lock()
	provider.transportCache[transportCacheKey(identity, endpoint)] = transport
	provider.transportMu.Unlock()
}

func (provider *Provider) transportForEndpoint(
	identity vowifi.SIMIdentity,
	endpoint pcscfEndpoint,
	transportHint string,
) string {
	if transportHint = strings.ToLower(strings.TrimSpace(transportHint)); transportHint != "" {
		return transportHint
	}
	if cached := provider.cachedTransport(identity, endpoint); cached != "" {
		return cached
	}
	if transport, selected := carrierTransportForIdentity(provider.config, identity); selected {
		return transport
	}
	if transport := strings.ToLower(strings.TrimSpace(provider.config.Transport)); transport != "" {
		return transport
	}
	return "tcp"
}

func (provider *Provider) logTransportFallback(identity vowifi.SIMIdentity, from, to string, err error) {
	provider.config.Logger.Warn("IMS P-CSCF did not respond; trying alternate SIP transport",
		"carrier_profile", vowifi.ResolveCarrierProfile(identity).ID,
		"from_transport", from, "to_transport", to, "error", err)
}

type identitySet struct {
	domain  string
	private string
	public  string
	user    string
}

type registrationRejectedError struct {
	statusCode    int
	message       string
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (err *registrationRejectedError) Error() string {
	return err.message
}

func (err *registrationRejectedError) Unwrap() error {
	return ErrRegistrationRejected
}

func registrationRejectionStatus(err error) (int, bool) {
	var rejected *registrationRejectedError
	if !errors.As(err, &rejected) {
		return 0, false
	}
	return rejected.statusCode, true
}

func registrationRetryAfter(err error) (time.Duration, bool) {
	var rejected *registrationRejectedError
	if !errors.As(err, &rejected) || !rejected.hasRetryAfter {
		return 0, false
	}
	return rejected.retryAfter, true
}

func deriveIdentities(identity vowifi.SIMIdentity, config Config) (identitySet, error) {
	imsi := strings.TrimSpace(identity.IMSI)
	if !digitsBetween(imsi, 5, 16) {
		return identitySet{}, errors.New("ims: SIM IMSI is unavailable or invalid")
	}
	profile := vowifi.ResolveCarrierProfile(identity)
	mcc := strings.TrimSpace(profile.RouteMCC)
	mnc := strings.TrimSpace(profile.RouteMNC)
	if mcc == "" || mnc == "" {
		mcc = strings.TrimSpace(identity.HomeMCC)
		mnc = strings.TrimSpace(identity.HomeMNC)
	}
	if !digitsBetween(mcc, 3, 3) || !digitsBetween(mnc, 2, 3) {
		return identitySet{}, errors.New("ims: home PLMN is unavailable or invalid")
	}
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	domain := fmt.Sprintf("ims.mnc%s.mcc%s.3gppnetwork.org", mnc, mcc)
	privateDomain := domain
	publicDomain := domain
	if profile.IMSIdentityProfile == vowifi.IMSProfileATT {
		// AT&T provisions the IMPI and IMPU in its ISIM domains rather than
		// the generic 3GPP PLMN IMS domain.
		domain = "one.att.net"
		privateDomain = "private.att.net"
		publicDomain = "one.att.net"
	}
	privateIdentity := config.PrivateIdentity
	if privateIdentity == "" {
		privateIdentity = imsi + "@" + privateDomain
	}
	publicIdentity := config.PublicIdentity
	if publicIdentity == "" {
		publicIdentity = "sip:" + imsi + "@" + publicDomain
	}
	if strings.ContainsAny(privateIdentity+publicIdentity, "\r\n") ||
		!strings.Contains(privateIdentity, "@") ||
		(!strings.HasPrefix(strings.ToLower(publicIdentity), "sip:") &&
			!strings.HasPrefix(strings.ToLower(publicIdentity), "sips:")) {
		return identitySet{}, errors.New("ims: configured IMS identity is invalid")
	}
	user := strings.TrimPrefix(strings.TrimPrefix(publicIdentity, "sip:"), "sips:")
	if at := strings.IndexByte(user, '@'); at >= 0 {
		user = user[:at]
	}
	if user == "" || strings.ContainsAny(user, "<>\" \t;") {
		return identitySet{}, errors.New("ims: public identity user is invalid")
	}
	return identitySet{domain: domain, private: privateIdentity, public: publicIdentity, user: user}, nil
}

type pcscfEndpoint struct {
	host string
	port int
}

func (endpoint pcscfEndpoint) address() string {
	return net.JoinHostPort(endpoint.host, strconv.Itoa(endpoint.port))
}

func parsePCSCF(raw string, defaultPort int) (pcscfEndpoint, string, error) {
	value := strings.Trim(strings.TrimSpace(raw), "<>")
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "sip:") {
		value = value[4:]
	} else if strings.HasPrefix(lower, "sips:") {
		value = value[5:]
	}
	transport := ""
	if separator := strings.IndexAny(value, ";?"); separator >= 0 {
		parameters := value[separator+1:]
		value = value[:separator]
		for _, parameter := range strings.FieldsFunc(parameters, func(character rune) bool {
			return character == ';' || character == '&'
		}) {
			key, parameterValue, found := strings.Cut(parameter, "=")
			if found && strings.EqualFold(strings.TrimSpace(key), "transport") {
				transport = strings.ToLower(strings.TrimSpace(parameterValue))
			}
		}
	}
	if at := strings.LastIndexByte(value, '@'); at >= 0 {
		value = value[at+1:]
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n/") {
		return pcscfEndpoint{}, "", errors.New("ims: invalid P-CSCF address")
	}

	host := value
	port := defaultPort
	if parsedHost, parsedPort, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
		numericPort, parseErr := strconv.Atoi(parsedPort)
		if parseErr != nil || numericPort < 1 || numericPort > 65535 {
			return pcscfEndpoint{}, "", errors.New("ims: invalid P-CSCF port")
		}
		port = numericPort
	} else if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		host = strings.Trim(value, "[]")
	} else if strings.Count(value, ":") == 1 {
		candidateHost, candidatePort, found := strings.Cut(value, ":")
		if found {
			numericPort, parseErr := strconv.Atoi(candidatePort)
			if parseErr != nil || numericPort < 1 || numericPort > 65535 {
				return pcscfEndpoint{}, "", errors.New("ims: invalid P-CSCF port")
			}
			host = candidateHost
			port = numericPort
		}
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || strings.ContainsAny(host, " \t<>\"") {
		return pcscfEndpoint{}, "", errors.New("ims: invalid P-CSCF host")
	}
	if transport != "" && transport != "udp" && transport != "tcp" {
		return pcscfEndpoint{}, "", fmt.Errorf("ims: unsupported P-CSCF transport %q", transport)
	}
	return pcscfEndpoint{host: host, port: port}, transport, nil
}

func pcscfProvenByTunnel(endpoint pcscfEndpoint, candidates []string, defaultPort int) bool {
	for _, candidate := range candidates {
		proven, _, err := parsePCSCF(candidate, defaultPort)
		if err != nil {
			continue
		}
		if proven.port == endpoint.port && equalHost(proven.host, endpoint.host) {
			return true
		}
	}
	return false
}

func equalHost(left string, right string) bool {
	leftIP := net.ParseIP(strings.Trim(left, "[]"))
	rightIP := net.ParseIP(strings.Trim(right, "[]"))
	if leftIP != nil || rightIP != nil {
		return leftIP != nil && rightIP != nil && leftIP.Equal(rightIP)
	}
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func localAddressProvenByTunnel(localAddress string, tunnel vowifi.TunnelEvidence) bool {
	localIP := net.ParseIP(strings.Trim(localAddress, "[]"))
	if localIP == nil {
		return false
	}
	for _, candidate := range []string{tunnel.LocalIPv4, tunnel.LocalIPv6} {
		candidate = strings.TrimSpace(strings.Split(candidate, "/")[0])
		candidateIP := net.ParseIP(strings.Trim(candidate, "[]"))
		if candidateIP != nil && localIP.Equal(candidateIP) {
			return true
		}
	}
	return false
}

func dialSIP(
	ctx context.Context,
	transport string,
	localAddress string,
	localPort int,
	remoteAddress string,
) (net.Conn, error) {
	var local net.Addr
	var err error
	switch transport {
	case "udp":
		local, err = net.ResolveUDPAddr("udp", net.JoinHostPort(localAddress, strconv.Itoa(localPort)))
	case "tcp":
		local, err = net.ResolveTCPAddr("tcp", net.JoinHostPort(localAddress, strconv.Itoa(localPort)))
	default:
		return nil, fmt.Errorf("ims: unsupported SIP transport %q", transport)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve tunnel local address: %w", err)
	}
	dialer := net.Dialer{LocalAddr: local}
	return dialer.DialContext(ctx, transport, remoteAddress)
}

type authenticationState struct {
	challenge digestChallenge
	response  []byte
	auts      string
	cnonce    string
	nc        uint32
}

type outboundRegistrationState uint8

const (
	outboundRegistrationDisabled outboundRegistrationState = iota
	outboundRegistrationRequested
	outboundRegistrationConfirmed
	outboundRegistrationFallback
)

type Session struct {
	provider            *Provider
	request             vowifi.IMSRequest
	identity            identitySet
	endpoint            pcscfEndpoint
	transport           string
	connMu              sync.RWMutex
	conn                net.Conn
	reader              *bufio.Reader
	sipLocalIP          net.IP
	pcscfIP             net.IP
	outboundFlowReady   chan struct{}
	outboundFlowChanged chan struct{}
	initialEndpoint     pcscfEndpoint
	securityDeclined    bool

	callID                 string
	fromTag                string
	instanceID             string
	outboundMu             sync.RWMutex
	outboundState          outboundRegistrationState
	outboundRegID          string
	pani                   string
	paniResolved           bool
	cseq                   uint32
	auth                   *authenticationState
	securityProposal       securityProposal
	securityAgreement      securityAgreement
	securityActive         bool          // compatibility aggregate for the complete SA set
	clientSecurityActive   bool          // c-flow SA pair is installed
	serverSecurityActive   bool          // s-flow SA pair is installed
	securityBootstrap      bool          // next REGISTER must start a fresh security negotiation
	ipsecHandle            IPSecSAHandle // compatibility handle for a complete SA set
	clientIPSecHandle      IPSecSAHandle
	serverIPSecHandle      IPSecSAHandle
	inboundTCPListener     *net.TCPListener
	inboundUDP             *net.UDPConn
	failures               chan error
	failureOnce            sync.Once
	registrationMu         sync.Mutex
	flowMu                 sync.Mutex
	recoveryMu             sync.Mutex
	recoveryRunning        bool
	recoveryDone           chan struct{}
	keepaliveMu            sync.Mutex
	keepaliveNegotiated    bool
	keepaliveInterval      time.Duration
	keepaliveRunning       bool
	keepaliveSentCount     atomic.Uint64
	keepalivePeerPingCount atomic.Uint64
	keepalivePongSentCount atomic.Uint64
	writeMu                sync.Mutex
	transactionsMu         sync.Mutex
	transactions           map[sipTransactionKey]sipTransaction
	runtimeStarted         bool
	receiveDone            sync.WaitGroup
	inboundMu              sync.Mutex
	inboundTCPConnections  map[net.Conn]struct{}
	smsMu                  sync.Mutex
	nextRPReference        byte
	callMu                 sync.Mutex
	calls                  map[string]*imsCall

	mu                  sync.Mutex
	closed              bool
	evidence            vowifi.IMSEvidence
	smsContactConfirmed bool
	smsFallbackLogged   bool
	expiresAt           time.Time
	refreshContext      context.Context
	refreshCancel       context.CancelFunc
	refreshDone         chan struct{}
	runtimeStates       chan vowifi.IMSRuntimeStateEvent
}

func (session *Session) outboundRegistrationRequested() bool {
	if session == nil || session.transport != "tcp" {
		return false
	}
	session.outboundMu.RLock()
	state := session.outboundState
	session.outboundMu.RUnlock()
	return state == outboundRegistrationRequested || state == outboundRegistrationConfirmed
}

func (session *Session) outboundRegistrationConfirmed() bool {
	if session == nil || session.transport != "tcp" {
		return false
	}
	session.outboundMu.RLock()
	confirmed := session.outboundState == outboundRegistrationConfirmed
	session.outboundMu.RUnlock()
	return confirmed
}

func (session *Session) outboundRegistrationID() string {
	if session == nil || session.transport != "tcp" {
		return ""
	}
	session.outboundMu.RLock()
	if session.outboundState != outboundRegistrationRequested &&
		session.outboundState != outboundRegistrationConfirmed {
		session.outboundMu.RUnlock()
		return ""
	}
	regID := session.outboundRegID
	session.outboundMu.RUnlock()
	return regID
}

func (session *Session) setOutboundRegistrationState(state outboundRegistrationState) {
	if session == nil {
		return
	}
	session.outboundMu.Lock()
	session.outboundState = state
	session.outboundMu.Unlock()
}

func (session *Session) currentOutboundFlow() (net.Conn, *bufio.Reader, <-chan struct{}, error) {
	if session == nil {
		return nil, nil, nil, errSIPConnectionUnavailable
	}
	session.connMu.RLock()
	connection, reader, changed := session.conn, session.reader, session.outboundFlowChanged
	session.connMu.RUnlock()
	if connection == nil {
		return nil, nil, changed, errSIPConnectionUnavailable
	}
	return connection, reader, changed, nil
}

func (session *Session) isCurrentOutboundFlow(connection net.Conn) bool {
	if session == nil || connection == nil {
		return false
	}
	session.connMu.RLock()
	defer session.connMu.RUnlock()
	return session.conn == connection
}

func (session *Session) waitForOutboundFlow(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		connection, reader, changed, err := session.currentOutboundFlow()
		if err == nil {
			return connection, reader, nil
		}
		// Any new outbound request after the c-flow disappeared uses the normal
		// recovery path. The recovery first reconnects through the active SA;
		// only a failed protected recovery falls back to fresh AKA/IPsec setup.
		session.startSIPFlowRecovery()
		session.connMu.RLock()
		ready := session.outboundFlowReady
		session.connMu.RUnlock()
		if ready == nil {
			ready = make(chan struct{})
		}
		var sessionDone <-chan struct{}
		if session.refreshContext != nil {
			sessionDone = session.refreshContext.Done()
		}
		select {
		case <-ready:
		case <-changed:
		case <-sessionDone:
			return nil, nil, ErrSessionClosed
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func (session *Session) installOutboundFlow(connection net.Conn) error {
	_, err := session.installOutboundFlowWithReader(connection, nil, true)
	return err
}

func (session *Session) installOutboundFlowWithReader(
	connection net.Conn,
	reader *bufio.Reader,
	closePrevious bool,
) (net.Conn, error) {
	if connection == nil {
		return nil, errSIPConnectionUnavailable
	}
	if session.transport == "tcp" && reader == nil {
		reader = bufio.NewReader(connection)
	}
	ready := make(chan struct{})
	close(ready)
	session.connMu.Lock()
	old := session.conn
	previousReady := session.outboundFlowReady
	previousChanged := session.outboundFlowChanged
	session.conn = connection
	session.reader = reader
	session.outboundFlowReady = ready
	session.outboundFlowChanged = make(chan struct{})
	if previousReady != nil {
		select {
		case <-previousReady:
		default:
			close(previousReady)
		}
	}
	if closePrevious && previousChanged != nil {
		close(previousChanged)
	}
	session.connMu.Unlock()
	if closePrevious && old != nil && old != connection {
		_ = old.Close()
	}
	return old, nil
}

// installOutboundFlowIfEmpty installs a newly dialled SIP flow only when
// another outbound has not won the race. The losing connection is left to
// the caller to close.
func (session *Session) installOutboundFlowIfEmpty(connection net.Conn) (*bufio.Reader, bool) {
	if connection == nil || session.isClosed() {
		return nil, false
	}
	return session.installOutboundFlowIfEmptyLocked(connection)
}

// installOutboundFlowIfEmptyLocked is used by the recovery transaction
// while session.mu is already held. The caller has already checked closed.
func (session *Session) installOutboundFlowIfEmptyLocked(connection net.Conn) (*bufio.Reader, bool) {
	if connection == nil {
		return nil, false
	}
	var reader *bufio.Reader
	if session.transport == "tcp" {
		reader = bufio.NewReader(connection)
	}
	session.connMu.Lock()
	if session.conn != nil {
		session.connMu.Unlock()
		return nil, false
	}
	ready := session.outboundFlowReady
	if ready == nil {
		ready = make(chan struct{})
		session.outboundFlowReady = ready
	}
	select {
	case <-ready:
	default:
		close(ready)
	}
	changed := session.outboundFlowChanged
	session.conn = connection
	session.reader = reader
	session.outboundFlowChanged = make(chan struct{})
	if changed != nil {
		close(changed)
	}
	session.connMu.Unlock()
	return reader, true
}

func (session *Session) detachOutboundFlow(connection net.Conn) bool {
	if session == nil || connection == nil {
		return false
	}
	session.connMu.Lock()
	if session.conn != connection {
		session.connMu.Unlock()
		return false
	}
	changed := session.outboundFlowChanged
	session.conn = nil
	session.reader = nil
	session.outboundFlowReady = make(chan struct{})
	session.outboundFlowChanged = make(chan struct{})
	if changed != nil {
		close(changed)
	}
	session.connMu.Unlock()
	_ = connection.Close()
	return true
}

func (session *Session) startSIPFlowRecovery() <-chan struct{} {
	if session == nil || session.transport != "tcp" || session.provider == nil ||
		len(session.sipLocalIP) == 0 || session.endpoint.host == "" {
		return nil
	}
	if _, _, _, err := session.currentOutboundFlow(); err == nil {
		return nil
	}
	session.recoveryMu.Lock()
	if session.recoveryRunning {
		done := session.recoveryDone
		session.recoveryMu.Unlock()
		return done
	}
	done := make(chan struct{})
	session.recoveryRunning = true
	session.recoveryDone = done
	session.recoveryMu.Unlock()
	go func() {
		defer func() {
			session.recoveryMu.Lock()
			if session.recoveryDone == done {
				session.recoveryRunning = false
				session.recoveryDone = nil
			}
			close(done)
			session.recoveryMu.Unlock()
		}()
		session.recoverSIPFlow()
	}()
	return done
}

func (session *Session) recoverSIPFlow() {
	ctx := session.refreshContext
	if ctx == nil {
		ctx = context.Background()
	}
	consecutiveFailures := 0
	for {
		if ctx.Err() != nil || session.isClosed() {
			return
		}
		if session.expireRegistrationIfNeeded() {
			return
		}

		_, _, _, connectionErr := session.currentOutboundFlow()
		requireNewFlow := connectionErr != nil
		// A TCP socket loss does not itself invalidate the active 3GPP SA set.
		// First reconnect with the protected client port. If that attempt fails,
		// the next attempt performs a complete security bootstrap.
		forceNewSecurity := requireNewFlow && session.securityOffered() && consecutiveFailures > 0
		if forceNewSecurity {
			session.mu.Lock()
			if session.closed {
				session.mu.Unlock()
				return
			}
			if _, _, _, currentErr := session.currentOutboundFlow(); currentErr == nil {
				requireNewFlow = false
				forceNewSecurity = false
			}
			session.mu.Unlock()
		}
		recoveryMode := "current_outbound"
		if forceNewSecurity {
			recoveryMode = "new_security"
		} else if requireNewFlow {
			recoveryMode = "new_outbound"
		}
		if err := session.reregisterRecoveredFlow(ctx, forceNewSecurity); err == nil {
			if err := session.startRecoveredOutboundFlowReader(); err != nil {
				if current, _, _, currentErr := session.currentOutboundFlow(); currentErr == nil {
					_ = session.detachOutboundFlow(current)
				}
				session.imsLogger().Warn("IMS recovered SIP flow could not start receiver",
					"category", "ims", "subsystem", "sip", "stage", "flow_recovery",
					"transport", session.transport, "error", err,
					"retry_in", sipFlowRecoveryDelay(consecutiveFailures),
					"consecutive_failures", consecutiveFailures+1)
				retryIn := sipFlowRecoveryDelay(consecutiveFailures)
				consecutiveFailures++
				if !session.waitForSIPFlowRecovery(ctx, retryIn) {
					session.expireRegistrationIfNeeded()
					return
				}
				continue
			}
			consecutiveFailures = 0
			session.imsLogger().Info("IMS SIP flow recovered and re-registered",
				"category", "ims", "subsystem", "sip", "stage", "flow_recovery",
				"transport", session.transport, "mode", recoveryMode)
			session.publishRuntimeState(vowifi.IMSRuntimeStateEvent{
				IMSReady:          true,
				SMSReady:          session.runtimeSMSReady(),
				RegistrationState: "registered",
				Reason:            "ims_flow_recovered",
			})
			return
		} else {
			if ctx.Err() != nil || errors.Is(err, ErrSessionClosed) {
				return
			}
			retryIn := sipFlowRecoveryDelay(consecutiveFailures)
			level := slog.LevelWarn
			if statusCode, ok := registrationRejectionStatus(err); ok && statusCode == 503 {
				// A temporary registrar rejection is retried without treating the
				// protected security context as failed.
				level = slog.LevelDebug
			}
			session.imsLogger().Log(context.Background(), level, "IMS SIP flow recovery attempt failed",
				"category", "ims", "subsystem", "sip", "stage", "flow_recovery",
				"transport", session.transport, "error", err,
				"mode", recoveryMode,
				"retry_in", retryIn,
				"consecutive_failures", consecutiveFailures+1)
			if current, _, _, currentErr := session.currentOutboundFlow(); currentErr == nil {
				// RFC 5626 treats a recoverable 503 as a registration failure,
				// not as proof that the existing TCP flow or IPsec context failed.
				// Keep that flow available for the next REGISTER attempt.
				if statusCode, ok := registrationRejectionStatus(err); !ok || statusCode != 503 {
					_ = session.detachOutboundFlow(current)
				}
			}
			consecutiveFailures++
			if !session.waitForSIPFlowRecovery(ctx, retryIn) {
				session.expireRegistrationIfNeeded()
				return
			}
		}
	}
}

func (session *Session) startRecoveredOutboundFlowReader() error {
	if session == nil || session.transport != "tcp" || !session.runtimeStarted {
		return nil
	}
	connection, reader, _, err := session.currentOutboundFlow()
	if err != nil {
		return err
	}
	if reader == nil {
		return errors.New("ims: recovered SIP TCP reader is unavailable")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("ims: clear recovered SIP connection deadline: %w", err)
	}
	session.receiveDone.Add(1)
	go session.readOutboundFlow(connection, reader)
	session.startSIPFlowKeepalive()
	return nil
}

func (session *Session) waitForSIPFlowRecovery(ctx context.Context, delay time.Duration) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if session != nil {
		session.mu.Lock()
		registered := session.evidence.Registered
		expiresAt := session.expiresAt
		session.mu.Unlock()
		if registered && !expiresAt.IsZero() {
			remaining := time.Until(expiresAt)
			if remaining <= 0 {
				return false
			}
			if remaining < delay {
				delay = remaining
			}
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func waitForSIPFlowRecoveryDone(ctx context.Context, done <-chan struct{}) bool {
	if done == nil {
		return true
	}
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (session *Session) reregisterRecoveredFlow(ctx context.Context, forceNewSecurity bool) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return ErrSessionClosed
	}
	session.evidence.RegistrationState = "flow_recovering"
	session.registrationMu.Lock()
	defer session.registrationMu.Unlock()
	defer session.mu.Unlock()
	completeRegistration := func(response *sipResponse) error {
		if response.StatusCode != 200 {
			return registrationRejectionError(response, "flow recovery")
		}
		timing, err := session.applyRegistrationEvidence(response)
		if err != nil {
			return err
		}
		session.logRegistrationSuccess("flow_recovery", response.StatusCode, timing)
		return nil
	}
	if _, _, _, connectionErr := session.currentOutboundFlow(); connectionErr != nil {
		if !forceNewSecurity && session.cFlowSecurityActive() {
			if err := session.reconnectProtectedSIPFlowLocked(ctx); err != nil {
				return err
			}
		} else {
			if err := session.prepareSIPFlowRecoveryLocked(ctx); err != nil {
				return err
			}
		}
	}

	response, err := session.registerOnNewFlow(ctx, int(session.provider.config.RegistrationExpiry/time.Second))
	if err != nil {
		return err
	}
	return completeRegistration(response)
}

// reconnectProtectedSIPFlowLocked recreates only the TCP socket over the
// active UE-client/P-CSCF-server SA pair. A socket EOF does not authorize
// changing ports, SPIs, keys, or the independent inbound flow.
func (session *Session) reconnectProtectedSIPFlowLocked(ctx context.Context) error {
	session.flowMu.Lock()
	defer session.flowMu.Unlock()

	if _, _, _, err := session.currentOutboundFlow(); err == nil {
		return nil
	}
	if !session.cFlowSecurityActive() || !validProtectedPort(session.securityProposal.portClient) ||
		!validProtectedPort(session.securityAgreement.selected.portServer) {
		return errors.New("ims: active protected c-flow context is unavailable")
	}

	dialContext, cancel := context.WithTimeout(ctx, session.provider.config.TransactionTimeout)
	connection, err := dialSIP(
		dialContext,
		session.transport,
		session.sipLocalIP.String(),
		session.securityProposal.portClient,
		net.JoinHostPort(session.pcscfIP.String(), strconv.Itoa(session.securityAgreement.selected.portServer)),
	)
	cancel()
	if err != nil {
		return fmt.Errorf("ims: reconnect protected SIP flow: %w", err)
	}
	if _, installed := session.installOutboundFlowIfEmptyLocked(connection); !installed {
		_ = connection.Close()
		if _, _, _, currentErr := session.currentOutboundFlow(); currentErr == nil {
			return nil
		}
		return errors.New("ims: recovered protected SIP flow was not installed")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = session.detachOutboundFlow(connection)
		return fmt.Errorf("ims: clear recovered protected SIP connection deadline: %w", err)
	}
	return nil
}

// prepareSIPFlowRecoveryLocked starts a complete security bootstrap after the
// active protected c-flow could not be re-established. The old s-flow remains
// available until the candidate full SA set succeeds; no individual old SA is
// replaced in place.
func (session *Session) prepareSIPFlowRecoveryLocked(ctx context.Context) error {
	session.flowMu.Lock()
	defer session.flowMu.Unlock()

	if _, _, _, err := session.currentOutboundFlow(); err == nil {
		return nil
	}
	if len(session.sipLocalIP) == 0 {
		return errors.New("ims: SIP flow recovery local IP address is unavailable")
	}

	if session.securityOffered() && validProtectedPort(session.securityProposal.portServer) {
		previous := session.securityProposal
		proposal, err := rotatedSecurityProposal(session.sipLocalIP, previous)
		if err != nil {
			return fmt.Errorf("ims: prepare rotated protected SIP flow: %w", err)
		}
		session.clientSecurityActive = false
		session.syncSecurityActive()
		session.securityBootstrap = session.securityOffered()
		session.endpoint = session.initialEndpoint
		session.securityProposal = proposal
		session.clearAuthentication()
		session.imsLogger().Info("IMS protected SIP security bootstrap prepared",
			"category", "ims", "subsystem", "sip", "stage", "flow_recovery",
			"transport", session.transport,
			"old_port_c", previous.portClient,
			"new_port_c", proposal.portClient,
			"port_s", proposal.portServer,
			"old_spi_c", previous.spiClient,
			"new_spi_c", proposal.spiClient,
			"old_spi_s", previous.spiServer,
			"new_spi_s", proposal.spiServer)
	} else {
		session.endpoint = session.initialEndpoint
		session.securityBootstrap = session.securityOffered()
	}

	dialContext, cancel := context.WithTimeout(ctx, session.provider.config.TransactionTimeout)
	connection, err := dialSIP(
		dialContext,
		session.transport,
		session.sipLocalIP.String(),
		0,
		session.initialEndpoint.address(),
	)
	cancel()
	if err != nil {
		return fmt.Errorf("ims: flow recovery SIP dial: %w", err)
	}
	if _, installed := session.installOutboundFlowIfEmptyLocked(connection); !installed {
		_ = connection.Close()
		if _, _, _, currentErr := session.currentOutboundFlow(); currentErr == nil {
			return nil
		}
		return errors.New("ims: flow recovery replacement connection was not installed")
	}
	if err := connection.SetDeadline(time.Time{}); err != nil {
		_ = session.detachOutboundFlow(connection)
		return fmt.Errorf("ims: clear recovered SIP connection deadline: %w", err)
	}
	return nil
}

func (session *Session) closeOutboundFlow() error {
	connection, _, _, err := session.currentOutboundFlow()
	if err != nil {
		return nil
	}
	return connection.Close()
}

func newSession(
	provider *Provider,
	request vowifi.IMSRequest,
	identity identitySet,
	endpoint pcscfEndpoint,
	transport string,
	connection net.Conn,
) (*Session, error) {
	callToken, err := randomHex(18)
	if err != nil {
		return nil, err
	}
	fromTag, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	instanceID, err := randomUUID()
	if err != nil {
		return nil, err
	}
	instanceURI := sipInstanceID(request.Identity, instanceID)
	refreshContext, refreshCancel := context.WithCancel(context.Background())
	outboundFlowReady := make(chan struct{})
	close(outboundFlowReady)
	outboundState := outboundRegistrationDisabled
	outboundRegID := ""
	if transport == "tcp" {
		outboundState = outboundRegistrationRequested
		outboundRegID = "1"
	}
	session := &Session{
		provider:            provider,
		request:             request,
		identity:            identity,
		endpoint:            endpoint,
		initialEndpoint:     endpoint,
		transport:           transport,
		conn:                connection,
		sipLocalIP:          append(net.IP(nil), addressIP(connection.LocalAddr())...),
		pcscfIP:             append(net.IP(nil), addressIP(connection.RemoteAddr())...),
		outboundFlowReady:   outboundFlowReady,
		outboundFlowChanged: make(chan struct{}),
		callID:              callToken + "@" + addressHost(connection.LocalAddr()),
		fromTag:             fromTag,
		instanceID:          instanceURI,

		outboundState:         outboundState,
		outboundRegID:         outboundRegID,
		pani:                  resolveSessionPAccessNetworkInfo(request.Identity, provider.config.Logger),
		paniResolved:          true,
		cseq:                  1,
		refreshContext:        refreshContext,
		refreshCancel:         refreshCancel,
		refreshDone:           make(chan struct{}),
		runtimeStates:         make(chan vowifi.IMSRuntimeStateEvent, 4),
		failures:              make(chan error, 1),
		transactions:          make(map[sipTransactionKey]sipTransaction),
		inboundTCPConnections: make(map[net.Conn]struct{}),
		calls:                 make(map[string]*imsCall),
		evidence: vowifi.IMSEvidence{
			RegistrationState: "registering",
			Transport:         transport,
		},
	}
	if transport == "tcp" {
		session.reader = bufio.NewReader(connection)
	}
	if provider.config.SecurityMode != SecurityDisabled {
		localIP := addressIP(connection.LocalAddr())
		if localIP == nil {
			refreshCancel()
			return nil, errors.New("ims: protected local IP address is unavailable")
		}
		protectedClientPort := provider.config.ProtectedClientPort
		protectedServerPort := provider.config.ProtectedServerPort
		if vowifi.IsATT310280(request.Identity) && protectedServerPort == 0 {
			protectedServerPort = 6000
		}
		if securityEncryptionForIdentity(request.Identity) == "null" {
			if protectedClientPort == 0 {
				protectedClientPort = 5062
			}
			if protectedServerPort == 0 {
				protectedServerPort = 5063
			}
		}
		proposal, err := newSecurityProposal(
			localIP,
			protectedClientPort,
			protectedServerPort,
		)
		if err != nil {
			refreshCancel()
			return nil, err
		}
		proposal.encryption = securityEncryptionForIdentity(request.Identity)
		if vowifi.IsATT310280(request.Identity) {
			proposal.integrityAlgorithms = []string{"hmac-sha-1-96"}
			proposal.encryptionAlgorithmsList = []string{"aes-cbc"}
		}
		session.securityProposal = proposal
		inboundTCPListener, err := net.ListenTCP(
			"tcp",
			&net.TCPAddr{IP: append(net.IP(nil), localIP...), Port: proposal.portServer},
		)
		if err != nil {
			refreshCancel()
			return nil, fmt.Errorf("ims: reserve protected TCP server port: %w", err)
		}
		inboundUDP, err := net.ListenUDP(
			"udp",
			&net.UDPAddr{IP: append(net.IP(nil), localIP...), Port: proposal.portServer},
		)
		if err != nil {
			_ = inboundTCPListener.Close()
			refreshCancel()
			return nil, fmt.Errorf("ims: reserve protected UDP server port: %w", err)
		}
		session.inboundTCPListener = inboundTCPListener
		session.inboundUDP = inboundUDP
	}
	return session, nil
}

func securityEncryptionForIdentity(identity vowifi.SIMIdentity) string {
	return vowifi.ResolveCarrierProfile(identity).IMSIPSecEncryption
}

func (session *Session) abort() {
	session.refreshCancel()
	_ = session.closeOutboundFlow()
	if session.inboundTCPListener != nil {
		_ = session.inboundTCPListener.Close()
	}
	if session.inboundUDP != nil {
		_ = session.inboundUDP.Close()
	}
	_ = session.closeProtectedSecurity(context.Background())
	session.clearAuthentication()
}

func (session *Session) establish(ctx context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	response, err := session.register(ctx, int(session.provider.config.RegistrationExpiry/time.Second))
	if err != nil {
		session.evidence.RegistrationState = "failed"
		return err
	}
	if response.StatusCode != 200 {
		session.evidence.RegistrationState = "rejected"
		phase := "initial"
		if session.auth != nil {
			phase = "authenticated"
		}
		return registrationRejectionError(response, phase)
	}
	timing, err := session.applyRegistrationEvidence(response)
	if err != nil {
		return err
	}
	if err := session.startRuntimeReceivers(); err != nil {
		return err
	}
	session.logRegistrationSuccess("registration_initial", response.StatusCode, timing)
	go session.refreshLoop()
	return nil
}

// registrationRejectionError preserves the registrar's safe diagnostic text.
// Operators commonly use the SIP reason phrase, Reason, or Warning header to
// distinguish an unprovisioned subscriber from a malformed REGISTER. Do not
// include authentication headers here because they contain AKA material.
// Return only the safe message and retry metadata that lifecycle code needs;
// the raw SIP response ends at this boundary.
func registrationRejectionError(response *sipResponse, phase string) error {
	if response == nil {
		return fmt.Errorf("%w: empty SIP response", ErrRegistrationRejected)
	}
	phase = strings.TrimSpace(phase)
	if phase == "" {
		phase = "unknown"
	}
	message := fmt.Sprintf(
		"%s: SIP %d",
		phase+" REGISTER was rejected",
		response.StatusCode,
	)
	if reason := safeSIPDiagnostic(response.Reason); reason != "" {
		message += " " + reason
	}
	for _, header := range []string{"Reason", "Warning"} {
		for _, value := range response.values(header) {
			if value = safeSIPDiagnostic(value); value != "" {
				message += fmt.Sprintf("; %s: %s", header, value)
			}
		}
	}
	// P-Debug-Info is carrier-generated but can contain subscriber identifiers.
	// Surface only a fixed classification for the O2 security-agreement error;
	// never copy the raw header into logs or API responses.
	for _, value := range response.values("P-Debug-Info") {
		if strings.Contains(strings.ToLower(value), "no matched security item") {
			message += "; carrier detail: no matched IMS security item"
			break
		}
	}
	retryAfter, hasRetryAfter := parseSIPRetryAfter(response.values("Retry-After"))
	return &registrationRejectedError{
		statusCode:    response.StatusCode,
		message:       message,
		retryAfter:    retryAfter,
		hasRetryAfter: hasRetryAfter,
	}
}

func parseSIPRetryAfter(values []string) (time.Duration, bool) {
	for _, value := range values {
		value = strings.TrimSpace(value)
		end := 0
		for end < len(value) && value[end] >= '0' && value[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		seconds, err := strconv.ParseInt(value[:end], 10, 64)
		if err != nil {
			continue
		}
		maximum := int64((24 * time.Hour) / time.Second)
		if seconds > maximum {
			seconds = maximum
		}
		return time.Duration(seconds) * time.Second, true
	}
	return 0, false
}

func safeSIPDiagnostic(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	const maximum = 256
	if len(value) > maximum {
		value = value[:maximum] + "..."
	}
	return value
}

func (session *Session) register(ctx context.Context, expires int) (*sipResponse, error) {
	return session.registerWithRuntimeOnFlow(ctx, expires, session.runtimeStarted, nil)
}

func (session *Session) registerOnNewFlow(ctx context.Context, expires int) (*sipResponse, error) {
	return session.registerWithRuntimeOnFlow(ctx, expires, false, nil)
}

func (session *Session) registerWithRuntime(ctx context.Context, expires int, runtimeStarted bool) (*sipResponse, error) {
	return session.registerWithRuntimeOnFlow(ctx, expires, runtimeStarted, nil)
}

func (session *Session) registerOnFlow(ctx context.Context, expires int, connection net.Conn) (*sipResponse, error) {
	return session.registerWithRuntimeOnFlow(ctx, expires, session.runtimeStarted, connection)
}

func (session *Session) registerWithRuntimeOnFlow(
	ctx context.Context,
	expires int,
	runtimeStarted bool,
	expectedConnection net.Conn,
) (*sipResponse, error) {
	registrationProposal := session.securityProposal
	if expires > 0 && session.cFlowSecurityActive() {
		var err error
		registrationProposal, err = rotatedSecurityProposal(session.sipLocalIP, session.securityProposal)
		if err != nil {
			return nil, fmt.Errorf("ims: prepare registration security proposal: %w", err)
		}
	}
	for challenges := 0; challenges <= maxAuthenticationChallenges; challenges++ {
		if expectedConnection != nil && !session.isCurrentOutboundFlow(expectedConnection) {
			return nil, errSIPConnectionUnavailable
		}
		cseq := session.cseq
		session.cseq++
		authorization := ""
		authorizationHeader := ""
		if session.auth != nil {
			session.auth.nc++
			credentials := digestCredentials{
				Username:    session.identity.private,
				AKAResponse: session.auth.response,
				AUTS:        session.auth.auts,
				URI:         "sip:" + session.identity.domain,
				Method:      "REGISTER",
				CNonce:      session.auth.cnonce,
				NC:          session.auth.nc,
			}
			authorization = buildDigestAuthorization(session.auth.challenge, credentials)
			if session.auth.challenge.Proxy {
				authorizationHeader = "Proxy-Authorization"
			} else {
				authorizationHeader = "Authorization"
			}
		}
		connection, _, _, err := session.currentOutboundFlow()
		if err != nil || (expectedConnection != nil && connection != expectedConnection) {
			return nil, errSIPConnectionUnavailable
		}
		request, err := session.buildRegisterFor(
			cseq,
			expires,
			authorizationHeader,
			authorization,
			connection,
			session.endpoint,
			registrationProposal,
			session.securityAgreement,
			session.cFlowSecurityActive(),
			session.securityBootstrap,
		)
		if err != nil {
			return nil, err
		}
		response, err := session.exchangeWithRuntimeOnFlow(ctx, request, runtimeStarted, expectedConnection)
		if err != nil {
			return nil, err
		}
		session.logRegistrationResponse(response)
		session.evidence.LastSIPCode = response.StatusCode
		if response.StatusCode == 439 && session.outboundRegistrationRequested() {
			// RFC 5626 permits falling back to an ordinary registration when
			// the first hop explicitly reports that it lacks outbound support.
			session.setOutboundRegistrationState(outboundRegistrationFallback)
			continue
		}
		if response.StatusCode != 401 && response.StatusCode != 407 {
			return response, nil
		}
		if challenges == maxAuthenticationChallenges {
			break
		}
		challenge, err := challengeFromResponse(response)
		if err != nil {
			return nil, err
		}
		preference := ""
		if vowifi.IsATT310280(session.request.Identity) {
			preference = "isim_strict"
		}
		if session.cFlowSecurityActive() {
			response, err := session.registerWithNewSecurity(
				ctx, expires, response, challenge, preference, expectedConnection, registrationProposal,
			)
			if err != nil {
				return nil, err
			}
			session.logRegistrationResponse(response)
			session.evidence.LastSIPCode = response.StatusCode
			return response, nil
		}
		agreement, useSecurity, err := session.securityFromResponse(response)
		if err != nil {
			return nil, err
		}
		material, err := authenticateAKA(ctx, session.provider.aka, session.request.Identity, challenge, preference)
		if err != nil {
			return nil, err
		}
		if len(material.auts) == 0 && useSecurity {
			if err := session.activateIPSec(ctx, agreement, material.ck, material.ik); err != nil {
				clearAKAMaterial(&material)
				return nil, err
			}
		}
		credentials, err := newDigestCredentials(
			session.identity.private,
			material.response,
			"sip:"+session.identity.domain,
			"REGISTER",
			1,
		)
		if err != nil {
			clearAKAMaterial(&material)
			return nil, err
		}
		auts := base64.StdEncoding.EncodeToString(material.auts)
		session.auth = &authenticationState{
			challenge: challenge,
			response:  append([]byte(nil), material.response...),
			auts:      auts,
			cnonce:    credentials.CNonce,
		}
		clearAKAMaterial(&material)
	}
	return nil, errors.New("ims: too many SIP authentication challenges")
}

func (session *Session) registerWithNewSecurity(
	ctx context.Context,
	expires int,
	challengeResponse *sipResponse,
	challenge digestChallenge,
	preference string,
	expectedConnection net.Conn,
	proposal securityProposal,
) (*sipResponse, error) {
	oldConnection, _, _, err := session.currentOutboundFlow()
	if err != nil || (expectedConnection != nil && oldConnection != expectedConnection) {
		return nil, errSIPConnectionUnavailable
	}
	agreement, err := parseSecurityAgreement(
		challengeResponse.values("Security-Server"),
		proposal,
	)
	if err != nil {
		return nil, err
	}
	material, err := authenticateAKA(ctx, session.provider.aka, session.request.Identity, challenge, preference)
	if err != nil {
		return nil, err
	}
	if len(material.auts) != 0 {
		clearAKAMaterial(&material)
		return nil, errors.New("ims: authenticated SIP security rotation received an AKA synchronization failure")
	}
	defer clearAKAMaterial(&material)

	installed, candidateConnection, err := session.installProtectedSecurityContext(
		ctx, oldConnection, proposal, agreement, material.ck, material.ik,
		IPSecCFlowPair, IPSecSFlowPair,
	)
	if err != nil {
		return nil, err
	}
	closeCandidate := true
	defer func() {
		if closeCandidate {
			_ = candidateConnection.Close()
			_ = installed.close(context.Background())
		}
	}()

	cseq := session.cseq
	session.cseq++
	credentials, err := newDigestCredentials(
		session.identity.private,
		material.response,
		"sip:"+session.identity.domain,
		"REGISTER",
		1,
	)
	if err != nil {
		return nil, err
	}
	authorization := buildDigestAuthorization(challenge, credentials)
	authorizationHeader := "Authorization"
	if challenge.Proxy {
		authorizationHeader = "Proxy-Authorization"
	}
	endpoint := session.endpoint
	endpoint.port = agreement.selected.portServer
	request, err := session.buildRegisterFor(
		cseq,
		expires,
		authorizationHeader,
		authorization,
		candidateConnection,
		endpoint,
		proposal,
		agreement,
		true,
		false,
	)
	if err != nil {
		return nil, err
	}
	response, candidateReader, err := session.exchangeCandidateRegister(ctx, candidateConnection, request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return response, nil
	}

	oldInstalled := installedIPSecContext{
		all:   session.ipsecHandle,
		cFlow: session.clientIPSecHandle,
		sFlow: session.serverIPSecHandle,
	}
	if err := candidateConnection.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("ims: clear candidate SIP connection deadline: %w", err)
	}
	previousConnection, err := session.installOutboundFlowWithReader(
		candidateConnection,
		candidateReader,
		false,
	)
	if err != nil {
		return nil, err
	}
	session.securityProposal = proposal
	session.securityAgreement = agreement
	session.clientSecurityActive = true
	session.serverSecurityActive = true
	session.clientIPSecHandle = installed.cFlow
	session.serverIPSecHandle = installed.sFlow
	session.securityActive = true
	session.securityBootstrap = false
	session.endpoint = endpoint
	session.ipsecHandle = installed.all
	session.auth = &authenticationState{
		challenge: challenge,
		response:  append([]byte(nil), material.response...),
		auts:      "",
		cnonce:    credentials.CNonce,
	}
	closeCandidate = false
	if session.runtimeStarted {
		session.receiveDone.Add(1)
		go session.readOutboundFlow(candidateConnection, candidateReader)
	}
	session.retirePreviousSecurityContext(previousConnection, oldInstalled)
	return response, nil
}

func (session *Session) retirePreviousSecurityContext(
	connection net.Conn,
	installed installedIPSecContext,
) {
	if connection == nil && installed.all == nil && installed.cFlow == nil && installed.sFlow == nil {
		return
	}
	session.receiveDone.Add(1)
	go func() {
		defer session.receiveDone.Done()
		ctx := session.refreshContext
		if ctx == nil {
			ctx = context.Background()
		}
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
	waitForTransactions:
		for connection != nil && session.hasTransactionsOnConnection(connection) {
			select {
			case <-ctx.Done():
				break waitForTransactions
			case <-ticker.C:
			}
		}
		if connection != nil {
			_ = connection.Close()
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		cleanupErr := installed.close(cleanupContext)
		cleanupCancel()
		if cleanupErr != nil {
			session.imsLogger().Warn("IMS previous SIP security context cleanup failed",
				"category", "ims", "subsystem", "sip", "stage", "security_rotation",
				"error", cleanupErr)
		}
	}()
}

func (session *Session) hasTransactionsOnConnection(connection net.Conn) bool {
	if connection == nil {
		return false
	}
	session.transactionsMu.Lock()
	defer session.transactionsMu.Unlock()
	for key := range session.transactions {
		if key.connection == connection {
			return true
		}
	}
	return false
}

func (session *Session) exchangeCandidateRegister(
	ctx context.Context,
	connection net.Conn,
	request []byte,
) (*sipResponse, *bufio.Reader, error) {
	if connection == nil {
		return nil, nil, errSIPConnectionUnavailable
	}
	key, err := sipClientTransactionKeyFromRequest(request, connection)
	if err != nil {
		return nil, nil, err
	}
	deadline := time.Now().Add(session.provider.config.TransactionTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, nil, fmt.Errorf("ims: set candidate SIP deadline: %w", err)
	}
	if _, err := connection.Write(request); err != nil {
		return nil, nil, fmt.Errorf("ims: send candidate SIP REGISTER: %w", err)
	}
	session.logRegistrationRequest(request)
	reader := bufio.NewReader(connection)
	for {
		response, err := readSIPResponse(reader)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, nil, contextErr
			}
			return nil, nil, fmt.Errorf("ims: receive candidate SIP REGISTER response: %w", err)
		}
		responseKey, keyErr := sipClientTransactionKey(response.value("Via"), response.value("CSeq"), connection)
		if keyErr != nil || responseKey != key {
			continue
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			continue
		}
		return response, reader, nil
	}
}

func challengeFromResponse(response *sipResponse) (digestChallenge, error) {
	header := "WWW-Authenticate"
	proxy := false
	if response.StatusCode == 407 {
		header = "Proxy-Authenticate"
		proxy = true
	}
	var lastError error
	for _, value := range response.values(header) {
		challenge, err := parseDigestChallenge(value, proxy)
		if err == nil {
			return challenge, nil
		}
		lastError = err
	}
	if lastError == nil {
		lastError = errors.New("ims: authentication response omitted a digest challenge")
	}
	return digestChallenge{}, lastError
}

func (session *Session) buildRegister(
	cseq uint32,
	expires int,
	authorizationHeader string,
	authorization string,
) ([]byte, error) {
	connection, _, _, err := session.currentOutboundFlow()
	if err != nil {
		return nil, err
	}
	return session.buildRegisterFor(
		cseq,
		expires,
		authorizationHeader,
		authorization,
		connection,
		session.endpoint,
		session.securityProposal,
		session.securityAgreement,
		session.cFlowSecurityActive(),
		session.securityBootstrap,
	)
}

func (session *Session) buildRegisterFor(
	cseq uint32,
	expires int,
	authorizationHeader string,
	authorization string,
	connection net.Conn,
	endpoint pcscfEndpoint,
	proposal securityProposal,
	agreement securityAgreement,
	securityActive bool,
	securityBootstrap bool,
) ([]byte, error) {
	profile := vowifi.ResolveCarrierProfile(session.request.Identity)
	registerOptions := profile.IMSRegisterOptions
	if registerOptions.ExpirySeconds != 0 {
		expires = registerOptions.ExpirySeconds
	}
	branch, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	local := connection.LocalAddr().String()
	contactAddress := session.contactAddressFor(connection, proposal)
	transportUpper := strings.ToUpper(session.transport)
	requestURI := "sip:" + session.identity.domain
	routeURI := "sip:" + endpoint.address() + ";transport=" + session.transport + ";lr"
	contact := session.buildContact(contactAddress, registerOptions)

	// Keep Supported and Allow tied to request handlers implemented by this
	// runtime. Carrier profiles may select identity and wire formatting, but
	// must not add or remove SIP capabilities that VoCat cannot actually use.
	// Path is a REGISTER routing capability; the UA may ignore the Path
	// returned in a REGISTER response. GRUU is not advertised because this
	// runtime does not consume pub-gruu/temp-gruu or route using ;gr. Without
	// that flow, advertising gruu would promise instance-routable Contact URIs
	// that the runtime cannot process. If RFC 5627 GRUU support is implemented
	// later, add gruu back only together with those response, URI, and routing
	// handlers; do not restore the declaration by profile configuration alone.
	defaultSupported := "path"
	if session.outboundRegistrationRequested() {
		defaultSupported += ", outbound"
	}
	// SUBSCRIBE and NOTIFY are event-package methods, not the inbound TCP
	// mechanism. VoCat has no corresponding event subscription/notification
	// handler, so they are intentionally not advertised in Allow.
	defaultAllow := "REGISTER, INVITE, ACK, CANCEL, BYE, OPTIONS, MESSAGE, PRACK, UPDATE"
	supported := defaultSupported
	allow := defaultAllow

	userAgent := session.imsUserAgent()

	via := fmt.Sprintf("SIP/2.0/%s %s;branch=z9hG4bK%s;rport", transportUpper, local, branch)
	if session.transport == "tcp" {
		// RFC 6223 uses Via;keep to negotiate keep-alives associated with
		// this registration. The parameter is not added to UDP because this
		// runtime does not implement the corresponding STUN keep-alive flow.
		via += ";keep"
	}

	lines := []string{
		"REGISTER " + requestURI + " SIP/2.0",
		"Via: " + via,
		"Max-Forwards: 70",
		"Route: <" + routeURI + ">",
		"From: <" + session.identity.public + ">;tag=" + session.fromTag,
		"To: <" + session.identity.public + ">",
		"Call-ID: " + session.callID,
		fmt.Sprintf("CSeq: %d REGISTER", cseq),
		"Contact: " + contact,
		fmt.Sprintf("Expires: %d", expires),
	}
	if supported != "" {
		lines = append(lines, "Supported: "+supported)
	}
	if allow != "" {
		lines = append(lines, "Allow: "+allow)
	}
	lines = append(lines, "User-Agent: "+userAgent)

	if registerOptions.PPreferredIdentity {
		lines = append(lines, "P-Preferred-Identity: <"+session.identity.public+">")
	}
	if value := strings.TrimSpace(registerOptions.PVisitedNetworkID); value != "" {
		lines = append(lines, `P-Visited-Network-ID: "`+value+`"`)
	}
	// PANI describes this UE's access and is stable for the complete IMS
	// session. The same UE-provided value is used by REGISTER, MESSAGE,
	// RP-ACK and dialog requests; it never claims to be network-provided.
	if pani := session.pAccessNetworkInfo(); pani != "" {
		lines = append(lines, "P-Access-Network-Info: "+pani)
	}
	if value := strings.TrimSpace(registerOptions.CellularNetworkInfo); value != "" {
		lines = append(lines, "Cellular-Network-Info: "+value)
	}

	acceptContactTags := []string{
		"*;+g.3gpp.smsip",
		`*;+g.3gpp.icsi-ref="urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"`,
	}
	if registerOptions.AcceptContactTags != nil {
		acceptContactTags = registerOptions.AcceptContactTags
	}
	for _, tag := range acceptContactTags {
		lines = append(lines, "Accept-Contact: "+tag)
	}

	if session.securityOffered() {
		lines = append(lines,
			"Security-Client: "+securityClientValueForIdentity(session.request.Identity, proposal),
			"Require: sec-agree",
			"Proxy-Require: sec-agree",
		)
		if securityActive {
			lines = append(lines, "Security-Verify: "+agreement.verifyValue)
		}
	}
	if authorization != "" {
		if session.securityOffered() {
			integrity := "no"
			if securityActive {
				integrity = "yes"
			}
			authorization += ", integrity-protected=" + integrity
		}
		lines = append(lines, authorizationHeader+": "+authorization)
	} else if (cseq == 1 || securityBootstrap) && session.securityOffered() {
		lines = append(lines, "Authorization: "+session.emptyDigestAuthorization())
	}
	lines = append(lines, "Content-Length: 0", "", "")
	return []byte(strings.Join(lines, "\r\n")), nil
}

func (session *Session) buildContact(contactAddress string, registerOptions vowifi.IMSRegisterOptions) string {
	base := fmt.Sprintf("<sip:%s@%s;transport=%s>", session.identity.user, contactAddress, session.transport)
	instanceID := session.instanceID
	icsiRef := "urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel"
	regID := ""
	if session.transport == "tcp" {
		if value := session.outboundRegistrationID(); value != "" {
			regID = ";reg-id=" + value
		}
	}

	switch registerOptions.ContactFormat {
	case vowifi.IMSContactFormatATT:
		extra := ""
		for _, tag := range registerOptions.ContactExtraTags {
			extra += ";" + tag
		}
		return fmt.Sprintf(
			`%s%s;audio;+g.3gpp.smsip;+g.3gpp.icsi-ref="%s";+sip.instance="<%s>"%s`,
			base, extra, icsiRef, instanceID, regID,
		)
	default:
		extra := ""
		for _, tag := range registerOptions.ContactExtraTags {
			extra += ";" + tag
		}
		return fmt.Sprintf(
			`%s;+sip.instance="<%s>"%s;+g.3gpp.smsip;audio;+g.3gpp.icsi-ref="%s"%s`,
			base, instanceID, regID, icsiRef, extra,
		)
	}
}

var sipInstanceICCIDNamespace = [16]byte{
	0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// sipInstanceID uses the standardized GSMA device-instance URI when a valid
// modem identity is available. ICCID is the stable UUIDv5 input when the
// modem does not expose an IMEI; the random value is only the final fallback.
func sipInstanceID(identity vowifi.SIMIdentity, fallback string) string {
	imei, _ := vowifi.ResolveIMSDeviceIdentity(identity)
	if len(imei) == 15 {
		valid := true
		for _, digit := range imei {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if valid {
			return "urn:gsma:imei:" + imei[:8] + "-" + imei[8:14] + "-0"
		}
	}
	if iccid := normalizedICCID(identity.ICCID); iccid != "" {
		return "urn:uuid:" + sipInstanceUUIDFromICCID(iccid)
	}
	return "urn:uuid:" + strings.TrimSpace(fallback)
}

func normalizedICCID(value string) string {
	var normalized strings.Builder
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			normalized.WriteRune(r)
		case r == ' ' || r == '-':
			continue
		default:
			return ""
		}
	}
	return normalized.String()
}

func sipInstanceUUIDFromICCID(iccid string) string {
	name := []byte("vocat:ims:iccid:" + iccid)
	input := make([]byte, len(sipInstanceICCIDNamespace)+len(name))
	copy(input, sipInstanceICCIDNamespace[:])
	copy(input[len(sipInstanceICCIDNamespace):], name)
	digest := sha1.Sum(input)
	digest[6] = (digest[6] & 0x0f) | 0x50
	digest[8] = (digest[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		digest[0:4], digest[4:6], digest[6:8], digest[8:10], digest[10:16],
	)
}

func (session *Session) imsRegisterOptions() vowifi.IMSRegisterOptions {
	if session == nil {
		return vowifi.IMSRegisterOptions{}
	}
	return vowifi.ResolveCarrierProfile(session.request.Identity).IMSRegisterOptions
}

func (session *Session) imsUserAgent() string {
	var fallbackUserAgent string
	if session != nil {
		profile := vowifi.ResolveCarrierProfile(session.request.Identity)
		if value := strings.TrimSpace(profile.IMSUserAgent); value != "" {
			return value
		}
		if session.provider != nil {
			if value := strings.TrimSpace(session.provider.config.UserAgent); value != "" {
				return value
			}
		}
		_, fallbackUserAgent = vowifi.ResolveIMSDeviceIdentity(session.request.Identity)
	}
	if fallbackUserAgent != "" {
		return fallbackUserAgent
	}
	if value := strings.TrimSpace(os.Getenv(imsUserAgentOverrideEnv)); value != "" {
		return value
	}
	return defaultSIPUserAgent
}

func (session *Session) imsLogger() *slog.Logger {
	if session != nil && session.provider != nil && session.provider.config.Logger != nil {
		return session.provider.config.Logger
	}
	return slog.Default()
}

// resolveSessionPAccessNetworkInfo freezes the selected value when the IMS
// session is created. This prevents a carrier-profile reload from changing
// access identity between REGISTER, SMS MESSAGE and its RP-ACK.
func resolveSessionPAccessNetworkInfo(identity vowifi.SIMIdentity, logger *slog.Logger) string {
	if logger == nil {
		logger = slog.Default()
	}
	profile := vowifi.ResolveCarrierProfile(identity)
	if profile.PANIEnabled != nil && !*profile.PANIEnabled {
		return ""
	}
	if configured := profile.IMSRegisterOptions.PAccessNetworkInfo; configured != nil {
		return appendPaniCountry(ueProvidedPANI(*configured), identity, profile, logger)
	}

	node := strings.ToLower(strings.TrimSpace(profile.PANINode))
	if decoded, err := hex.DecodeString(node); err != nil || len(decoded) != 6 {
		node = defaultPANIWLANNode
	}
	if node == "" {
		return ""
	}
	value := "IEEE-802.11;i-wlan-node-id=" + node
	return appendPaniCountry(value, identity, profile, logger)
}

func appendPaniCountry(value string, identity vowifi.SIMIdentity, profile vowifi.CarrierProfile, logger *slog.Logger) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	parts := strings.Split(value, ";")
	for _, parameter := range parts {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(parameter)), "country=") {
			return value
		}
	}

	countryMode := strings.ToUpper(strings.TrimSpace(profile.PANICountry))
	country := countryMode
	if countryMode == "AUTO" {
		mcc := strings.TrimSpace(identity.HomeMCC)
		if mcc == "" {
			mcc = strings.TrimSpace(profile.RouteMCC)
		}
		country = vowifi.CountryCodeForMCC(mcc)
		if country == "" {
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("IMS PANI country code could not be derived",
				"category", "ims",
				"stage", "pani_country",
				"carrier_profile", profile.ID,
				"mcc", mcc,
			)
			return value
		}
	}
	if country == "" {
		return value
	}
	parts = append(parts, "")
	copy(parts[2:], parts[1:])
	parts[1] = "country=" + country
	return strings.Join(parts, ";")
}

// ueProvidedPANI removes the network-provided marker from a profile override.
// RFC 7315 reserves that marker for a trusted proxy; a UE must not assert it.
func ueProvidedPANI(value string) string {
	parts := strings.Split(strings.TrimSpace(value), ";")
	filtered := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || strings.EqualFold(part, "network-provided") {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, ";")
}

func (session *Session) exchange(ctx context.Context, request []byte) (*sipResponse, error) {
	return session.exchangeWithRuntime(ctx, request, session.runtimeStarted)
}

func (session *Session) exchangeWithRuntime(
	ctx context.Context,
	request []byte,
	runtimeStarted bool,
) (*sipResponse, error) {
	return session.exchangeWithRuntimeOnFlow(ctx, request, runtimeStarted, nil)
}

func (session *Session) exchangeWithRuntimeOnFlow(
	ctx context.Context,
	request []byte,
	runtimeStarted bool,
	expectedConnection net.Conn,
) (*sipResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expectedConnection != nil && !session.isCurrentOutboundFlow(expectedConnection) {
		return nil, errSIPConnectionUnavailable
	}
	if runtimeStarted {
		return session.exchangeRuntimeOnFlow(ctx, request, expectedConnection)
	}
	connection, reader, _, err := session.currentOutboundFlow()
	if err != nil {
		return nil, err
	}
	if expectedConnection != nil && connection != expectedConnection {
		return nil, errSIPConnectionUnavailable
	}
	key, err := sipClientTransactionKeyFromRequest(request, connection)
	if err != nil {
		return nil, err
	}
	transactionDeadline := time.Now().Add(session.provider.config.TransactionTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(transactionDeadline) {
		transactionDeadline = contextDeadline
	}
	setReadDeadline := func(deadline time.Time) error {
		if err := connection.SetDeadline(deadline); err != nil {
			return fmt.Errorf("ims: set SIP transaction deadline: %w", err)
		}
		return nil
	}
	retransmitInterval := time.Duration(0)
	readDeadline := transactionDeadline
	if session.transport == "udp" {
		retransmitInterval = sipMessageRetransmitT1
		if candidate := time.Now().Add(retransmitInterval); candidate.Before(readDeadline) {
			readDeadline = candidate
		}
	}
	if err := setReadDeadline(readDeadline); err != nil {
		return nil, err
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = connection.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if expectedConnection != nil && !session.isCurrentOutboundFlow(connection) {
		return nil, errSIPConnectionUnavailable
	}
	_, writeErr := connection.Write(request)
	if writeErr != nil {
		session.detachOutboundFlow(connection)
		return nil, fmt.Errorf("ims: send SIP REGISTER: %w", writeErr)
	}
	session.logRegistrationRequest(request)

	retransmissions := 0
	for {
		var response *sipResponse
		var err error
		if session.transport == "tcp" {
			response, err = readSIPResponse(reader)
		} else {
			packet := make([]byte, 65535)
			var count int
			count, err = connection.Read(packet)
			if err == nil {
				response, err = parseSIPResponse(packet[:count])
			}
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			var networkErr net.Error
			if retransmitInterval > 0 && errors.As(err, &networkErr) && networkErr.Timeout() &&
				time.Now().Before(transactionDeadline) {
				retransmitInterval *= 2
				if retransmitInterval > sipMessageRetransmitMax {
					retransmitInterval = sipMessageRetransmitMax
				}
				nextDeadline := time.Now().Add(retransmitInterval)
				if nextDeadline.After(transactionDeadline) {
					nextDeadline = transactionDeadline
				}
				// The previous read deadline has already expired and net.Conn applies
				// it to writes too. Extend it before retransmitting.
				if deadlineErr := setReadDeadline(nextDeadline); deadlineErr != nil {
					return nil, deadlineErr
				}
				if _, writeErr := connection.Write(request); writeErr != nil {
					session.detachOutboundFlow(connection)
					return nil, fmt.Errorf("ims: retransmit SIP REGISTER: %w", writeErr)
				}
				retransmissions++
				continue
			}
			return nil, fmt.Errorf("ims: receive SIP REGISTER response: %w", err)
		}
		responseKey, keyErr := sipClientTransactionKey(response.value("Via"), response.value("CSeq"), connection)
		if keyErr != nil || responseKey != key {
			continue
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			retransmitInterval = 0
			if err := setReadDeadline(transactionDeadline); err != nil {
				return nil, err
			}
			continue
		}
		return response, nil
	}
}

func (session *Session) applyRegistrationEvidence(response *sipResponse) (registrationTimingObservation, error) {
	if session.provider.config.SecurityMode == SecurityRequired && !session.protectedSecurityActive() {
		session.evidence.Registered = false
		session.evidence.RegistrationState = "security_failed"
		return registrationTimingObservation{}, ErrIPSecAgreementRequired
	}
	associated := splitHeaderValues(response.values("P-Associated-URI"))
	contacts := splitHeaderValues(response.values("Contact"))
	serviceRoutes := splitHeaderValues(response.values("Service-Route"))
	registeredContact := ""
	smsConfirmed := false
	instanceLower := strings.ToLower(session.instanceID)
	contactURILower := strings.ToLower(fmt.Sprintf(
		"sip:%s@%s;transport=%s",
		session.identity.user,
		session.contactAddress(),
		session.transport,
	))
	for _, contact := range contacts {
		lower := strings.ToLower(contact)
		matchesThisSession := strings.Contains(lower, instanceLower) ||
			strings.Contains(lower, contactURILower)
		if matchesThisSession {
			registeredContact = contact
			smsConfirmed = strings.Contains(lower, "+g.3gpp.smsip")
			if strings.Contains(lower, instanceLower) {
				break
			}
		}
	}
	expiryObservation := registrationExpiryObservationFor(response, contacts, session.provider.config.RegistrationExpiry)
	expiry := expiryObservation.effective
	if expiry <= 0 {
		session.evidence.Registered = false
		session.evidence.RegistrationState = "rejected_zero_expiry"
		session.evidence.LastSIPCode = response.StatusCode
		session.clearAuthentication()
		return registrationTimingObservation{}, fmt.Errorf("%w: registrar granted zero expiry", ErrRegistrationRejected)
	}
	keepaliveDetails := parseSIPViaKeepaliveDetails(response.values("Via"))
	keepaliveNegotiated, keepaliveInterval := keepaliveDetails.negotiated, keepaliveDetails.interval
	keepaliveAdopted := session.transport == "tcp" && keepaliveNegotiated
	outboundRequested := session.outboundRegistrationRequested()
	outboundRegID := session.outboundRegistrationID()
	outboundConfirmed := outboundRequested && hasSIPOptionTag(response.values("Require"), "outbound")
	outboundServerSupported := append([]string(nil), response.values("Supported")...)
	outboundServerRequire := append([]string(nil), response.values("Require")...)
	if outboundRequested {
		if outboundConfirmed {
			session.setOutboundRegistrationState(outboundRegistrationConfirmed)
		} else {
			// RFC 5626 confirms outbound with Require: outbound. A successful
			// response without that confirmation falls back to ordinary SIP
			// registration for subsequent requests.
			session.setOutboundRegistrationState(outboundRegistrationFallback)
		}
	}
	session.keepaliveMu.Lock()
	session.keepaliveNegotiated = keepaliveAdopted
	session.keepaliveInterval = keepaliveInterval
	session.keepaliveMu.Unlock()
	if session.runtimeStarted {
		session.startSIPFlowKeepalive()
	}
	session.expiresAt = time.Now().Add(expiry)
	session.smsContactConfirmed = smsConfirmed
	session.evidence = vowifi.IMSEvidence{
		Registered:           true,
		RegistrationState:    "registered",
		PAssociatedURI:       append([]string(nil), associated...),
		AssociatedIdentities: append([]string(nil), associated...),
		RegisteredContact:    registeredContact,
		ServiceRoute:         append([]string(nil), serviceRoutes...),
		Transport:            session.transport,
		LastSIPCode:          response.StatusCode,
		SecurityMode:         session.effectiveSecurityMode(),
		SecurityVerified:     session.protectedSecurityActive(),
	}
	session.securityBootstrap = false
	// Keep the current AKA/Digest state for preemptive registration refreshes.
	// Full session teardown, security rotation, and failed refreshes still clear
	// it explicitly when the existing authentication context is no longer valid.
	return registrationTimingObservation{
		expiry:            expiryObservation,
		keepalive:         keepaliveDetails,
		keepaliveAdopted:  keepaliveAdopted,
		keepaliveInterval: keepaliveInterval,
		outboundRequested: outboundRequested,
		outboundConfirmed: outboundConfirmed,
		outboundRegID:     outboundRegID,
		outboundSupported: outboundServerSupported,
		outboundRequire:   outboundServerRequire,
	}, nil
}

func (session *Session) refreshLoop() {
	defer close(session.refreshDone)
	consecutiveTemporaryRejections := 0
	var retryDelay time.Duration
	for {
		session.mu.Lock()
		if session.closed || !session.evidence.Registered {
			session.mu.Unlock()
			return
		}
		remaining := time.Until(session.expiresAt)
		if remaining <= 0 {
			session.failRefresh()
			session.mu.Unlock()
			session.publishFailure(ErrRegistrationExpired)
			return
		}
		delay := retryDelay
		if delay <= 0 {
			delay = refreshDelay(remaining)
		}
		retryDelay = 0
		session.mu.Unlock()

		timer := time.NewTimer(delay)
		select {
		case <-session.refreshContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
		if _, _, _, flowErr := session.currentOutboundFlow(); flowErr != nil {
			if !session.outboundRegistrationConfirmed() {
				// Without registrar confirmation of RFC 5626 outbound, an EOF on
				// the UE-initiated flow is allowed to leave the inbound flow and
				// current security context in place until the next refresh.
				done := session.startSIPFlowRecovery()
				if done != nil {
					if !waitForSIPFlowRecoveryDone(session.refreshContext, done) {
						return
					}
					// The recovery worker performs the authenticated REGISTER and
					// updates the expiry before it completes.
					continue
				}
			}
			if _, _, err := session.waitForOutboundFlow(session.refreshContext); err != nil {
				return
			}
		}
		if err := session.refreshOnce(session.refreshContext); err != nil {
			if errors.Is(err, errSIPConnectionUnavailable) {
				session.logRegistrationRefreshOutcome(slog.LevelDebug, "flow_unavailable", err, 0)
				continue
			}
			if statusCode, ok := registrationRejectionStatus(err); ok && statusCode == 503 {
				delay, ok := session.scheduleRegistrationRefreshRetry(err, consecutiveTemporaryRejections)
				if !ok {
					session.logRegistrationRefreshOutcome(
						slog.LevelWarn,
						"retry_not_scheduled",
						err,
						0,
					)
					return
				}
				session.logRegistrationRefreshOutcome(
					slog.LevelWarn,
					"retry_scheduled",
					err,
					delay,
				)
				consecutiveTemporaryRejections++
				retryDelay = delay
				continue
			}
			session.logRegistrationRefreshOutcome(slog.LevelWarn, "failed", err, 0)
			session.publishFailure(err)
			return
		}
		consecutiveTemporaryRejections = 0
	}
}

func registrationRefreshRetryDelay(err error, consecutiveFailures int) time.Duration {
	if retryAfter, ok := registrationRetryAfter(err); ok {
		if retryAfter < time.Second {
			return time.Second
		}
		return retryAfter
	}
	delay := defaultRegistrationRetry
	for index := 0; index < consecutiveFailures && delay < maxRegistrationRetry; index++ {
		if delay > maxRegistrationRetry/2 {
			return maxRegistrationRetry
		}
		delay *= 2
	}
	if delay > maxRegistrationRetry {
		return maxRegistrationRetry
	}
	return delay
}

func (session *Session) scheduleRegistrationRefreshRetry(err error, consecutiveFailures int) (time.Duration, bool) {
	retryDelay := registrationRefreshRetryDelay(err, consecutiveFailures)
	session.mu.Lock()
	if session.closed || !session.evidence.Registered || session.expiresAt.IsZero() {
		session.mu.Unlock()
		return 0, false
	}
	remaining := time.Until(session.expiresAt)
	safety := registrationSafetyMargin
	if session.provider != nil {
		safety += session.provider.config.TransactionTimeout
	}
	if remaining <= safety {
		session.failRefresh()
		session.mu.Unlock()
		session.publishFailure(fmt.Errorf("ims: registration expired after temporary SIP rejection: %w", err))
		return 0, false
	}
	if retryDelay > remaining-safety {
		retryDelay = remaining - safety
	}
	if retryDelay <= 0 {
		session.failRefresh()
		session.mu.Unlock()
		session.publishFailure(fmt.Errorf("ims: registration expired after temporary SIP rejection: %w", err))
		return 0, false
	}
	session.evidence.RegistrationState = "refresh_retrying"
	session.mu.Unlock()

	return retryDelay, true
}

func refreshDelay(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	if remaining <= registrationSafetyMargin {
		return 100 * time.Millisecond
	}
	delay := temporaryRegistrationRefreshInterval
	if delay > remaining-registrationSafetyMargin {
		delay = remaining - registrationSafetyMargin
	}
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	return delay
}

func (session *Session) refreshOnce(ctx context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.registrationMu.Lock()
	defer session.registrationMu.Unlock()
	switch {
	case session.closed:
		return ErrSessionClosed
	case !session.evidence.Registered:
		return vowifi.ErrIMSNotRegistered
	}
	connection, _, _, err := session.currentOutboundFlow()
	if err != nil {
		return err
	}
	response, err := session.registerOnFlow(ctx, int(session.provider.config.RegistrationExpiry/time.Second), connection)
	if err != nil {
		if errors.Is(err, errSIPConnectionUnavailable) {
			return err
		}
		session.failRefresh()
		return fmt.Errorf("ims: refresh registration: %w", err)
	}
	if response.StatusCode != 200 {
		if response.StatusCode == 503 {
			// A temporary registrar rejection does not revoke an otherwise
			// valid binding or invalidate the protected SIP/IPsec context.
			session.evidence.RegistrationState = "refresh_retrying"
			return registrationRejectionError(response, "refresh")
		}
		session.failRefresh()
		return registrationRejectionError(response, "refresh")
	}
	timing, err := session.applyRegistrationEvidence(response)
	if err != nil {
		session.failRefresh()
		return err
	}
	session.logRegistrationSuccess("registration_refresh", response.StatusCode, timing)
	return nil
}

func (session *Session) failRefresh() {
	session.evidence.Registered = false
	session.evidence.RegistrationState = "refresh_failed"
	session.smsContactConfirmed = false
	session.expiresAt = time.Time{}
	session.clearAuthentication()
}

func (session *Session) expireRegistrationIfNeeded() bool {
	if session == nil {
		return false
	}
	session.mu.Lock()
	if session.closed || !session.evidence.Registered || session.expiresAt.IsZero() || time.Now().Before(session.expiresAt) {
		session.mu.Unlock()
		return false
	}
	session.failRefresh()
	session.mu.Unlock()
	session.publishFailure(ErrRegistrationExpired)
	return true
}

func (session *Session) publishFailure(err error) {
	if err == nil {
		return
	}
	session.failureOnce.Do(func() {
		session.failures <- err
	})
}

func (session *Session) runtimeSMSReady() bool {
	if session == nil {
		return false
	}
	session.mu.Lock()
	registered := session.evidence.Registered
	confirmed := session.smsContactConfirmed
	identity := session.request.Identity
	session.mu.Unlock()
	if !registered {
		return false
	}
	return confirmed || vowifi.ResolveCarrierProfile(identity).AllowSMSWithoutContactConfirmation
}

func (session *Session) publishRuntimeState(event vowifi.IMSRuntimeStateEvent) {
	if session == nil || session.runtimeStates == nil {
		return
	}
	select {
	case session.runtimeStates <- event:
	default:
		// State events are snapshots. If a slow consumer fills the small
		// buffer, discard the oldest snapshot and retain the newest one.
		select {
		case <-session.runtimeStates:
		default:
		}
		select {
		case session.runtimeStates <- event:
		default:
		}
	}
}

func (session *Session) RuntimeStates() <-chan vowifi.IMSRuntimeStateEvent {
	if session == nil {
		return nil
	}
	return session.runtimeStates
}

func (session *Session) Failures() <-chan error {
	return session.failures
}

func (session *Session) clearAuthentication() {
	if session.auth == nil {
		return
	}
	for index := range session.auth.response {
		session.auth.response[index] = 0
	}
	session.auth = nil
}

type registrationExpiryObservation struct {
	effective             time.Duration
	source                string
	contactExpiresSeconds []int
	expiresHeader         string
	expiresHeaderSeconds  int
}

type registrationTimingObservation struct {
	expiry            registrationExpiryObservation
	keepalive         sipViaKeepaliveDetails
	keepaliveAdopted  bool
	keepaliveInterval time.Duration
	outboundRequested bool
	outboundConfirmed bool
	outboundRegID     string
	outboundSupported []string
	outboundRequire   []string
}

func registrationExpiryObservationFor(response *sipResponse, contacts []string, fallback time.Duration) registrationExpiryObservation {
	observation := registrationExpiryObservation{source: "local_fallback", effective: fallback}
	for _, contact := range contacts {
		if seconds, ok := parameterSeconds(contact, "expires"); ok {
			observation.contactExpiresSeconds = append(observation.contactExpiresSeconds, seconds)
			if observation.source == "local_fallback" {
				observation.effective = boundedExpiry(seconds)
				observation.source = "contact_expires"
			}
		}
	}
	if response != nil {
		expiresValues := response.values("Expires")
		if len(expiresValues) > 0 {
			observation.expiresHeader = strings.TrimSpace(expiresValues[0])
			if seconds, err := strconv.Atoi(observation.expiresHeader); err == nil {
				observation.expiresHeaderSeconds = seconds
				if observation.source == "local_fallback" {
					observation.effective = boundedExpiry(seconds)
					observation.source = "expires_header"
				}
			}
		}
	}
	return observation
}

func (session *Session) logRegistrationResponse(response *sipResponse) {
	/* Full REGISTER response header logging is disabled for normal builds.
	if session == nil || response == nil || len(response.rawHeaders) == 0 {
		return
	}
	session.imsLogger().Debug("IMS SIP registration server response",
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"transport", session.transport,
		"flow", outboundFlowName,
		"status_code", response.StatusCode,
		"raw_headers", string(response.rawHeaders),
	)
	*/
}

func (session *Session) logRegistrationRequest(request []byte) {
	/* Full REGISTER request header logging is disabled for normal builds.
	if session == nil || len(request) == 0 {
		return
	}
	parsed, err := parseSIPPacket(request)
	if err != nil || parsed.Request == nil {
		return
	}
	sipRequest := parsed.Request
	if sipRequest.Method != "REGISTER" {
		return
	}
	session.imsLogger().Debug("IMS SIP registration request sent",
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"transport", session.transport,
		"flow", outboundFlowName,
		"method", sipRequest.Method,
		"cseq", sipRequest.value("CSeq"),
		"contact", sipRequest.value("Contact"),
		"user_agent", sipRequest.value("User-Agent"),
		"raw_headers", string(sipRequest.rawHeaders),
	)
	*/
}

func (session *Session) logRegistrationRefreshOutcome(
	level slog.Level,
	action string,
	err error,
	retryIn time.Duration,
) {
	if session == nil {
		return
	}
	attributes := []any{
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"stage", "registration_refresh",
		"transport", session.transport,
		"flow", outboundFlowName,
		"action", action,
	}
	if statusCode, ok := registrationRejectionStatus(err); ok {
		attributes = append(attributes, "status_code", statusCode)
	}
	if retryAfter, ok := registrationRetryAfter(err); ok {
		attributes = append(attributes, "retry_after_seconds", int(retryAfter/time.Second))
	}
	if retryIn > 0 {
		attributes = append(attributes, "retry_in_seconds", int(retryIn/time.Second))
	}
	if err != nil {
		attributes = append(attributes, "error", err)
	}
	session.imsLogger().Log(context.Background(), level, "IMS SIP registration refresh result", attributes...)
}

func (session *Session) logRegistrationSuccess(stage string, statusCode int, timing registrationTimingObservation) {
	if session == nil || strings.TrimSpace(stage) == "" {
		return
	}
	event := "IMS SIP registration succeeded"
	if stage == "registration_refresh" {
		event = "IMS SIP refresh registration succeeded"
	}
	adopted, intervalSeconds, policy := effectiveSIPKeepalivePolicy(
		session.transport,
		timing.keepaliveAdopted,
		timing.keepaliveInterval,
	)
	session.imsLogger().Info(event,
		"category", "ims",
		"subsystem", "sip",
		"device_id", session.request.DeviceID,
		"stage", stage,
		"flow", outboundFlowName,
		"status_code", statusCode,
		slog.Group("server",
			slog.Group("expiry",
				"contact_expires_seconds", timing.expiry.contactExpiresSeconds,
				"expires_header", timing.expiry.expiresHeader,
				"expires_header_seconds", timing.expiry.expiresHeaderSeconds,
			),
			slog.Group("keepalive",
				"value", timing.keepalive.rawValue,
				"interval_seconds", int(timing.keepalive.interval/time.Second),
			),
			slog.Group("outbound",
				"supported", timing.outboundSupported,
				"require", timing.outboundRequire,
			),
		),
		slog.Group("effective",
			"instance_id", session.instanceID,
			"transport", session.transport,
			"contact_format", session.imsRegisterOptions().ContactFormat,
			"pani", session.pAccessNetworkInfo(),
			slog.Group("expiry",
				"interval_seconds", int(timing.expiry.effective/time.Second),
				"source", timing.expiry.source,
			),
			slog.Group("keepalive",
				"adopted", adopted,
				"interval_seconds", intervalSeconds,
				"policy", policy,
			),
			slog.Group("outbound",
				"requested", timing.outboundRequested,
				"confirmed", timing.outboundConfirmed,
				"reg_id", timing.outboundRegID,
			),
		),
	)
}

func registrationExpiry(response *sipResponse, contacts []string, fallback time.Duration) time.Duration {
	return registrationExpiryObservationFor(response, contacts, fallback).effective
}

func parameterSeconds(value string, name string) (int, bool) {
	lower := strings.ToLower(value)
	needle := strings.ToLower(name) + "="
	index := strings.Index(lower, needle)
	if index < 0 {
		return 0, false
	}
	start := index + len(needle)
	end := start
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	seconds, err := strconv.Atoi(value[start:end])
	return seconds, err == nil
}

func boundedExpiry(seconds int) time.Duration {
	if seconds <= 0 {
		return 0
	}
	expiry := time.Duration(seconds) * time.Second
	if expiry > 24*time.Hour {
		return 24 * time.Hour
	}
	return expiry
}

func (session *Session) Evidence() vowifi.IMSEvidence {
	session.mu.Lock()
	defer session.mu.Unlock()
	evidence := cloneEvidence(session.evidence)
	if session.closed {
		evidence.Registered = false
		evidence.RegistrationState = "closed"
	} else if evidence.Registered && !session.expiresAt.IsZero() && !time.Now().Before(session.expiresAt) {
		evidence.Registered = false
		evidence.RegistrationState = "expired"
	}
	return evidence
}

func (session *Session) EnableSMS(ctx context.Context) (vowifi.SMSEvidence, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return vowifi.SMSEvidence{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	// This method is deliberately evidence-only. It never sends SIP MESSAGE,
	// modem CMGS commands, or any dial request. A registrar-confirmed feature
	// tag on this session's own Contact is the minimum readiness proof.
	switch {
	case session.closed:
		return vowifi.SMSEvidence{}, ErrSessionClosed
	case !session.evidence.Registered:
		return vowifi.SMSEvidence{}, vowifi.ErrIMSNotRegistered
	case !session.expiresAt.IsZero() && !time.Now().Before(session.expiresAt):
		return vowifi.SMSEvidence{}, ErrRegistrationExpired
	case !session.smsCapabilityReady():
		return vowifi.SMSEvidence{Ready: false}, ErrSMSCapabilityNotConfirmed
	default:
		return vowifi.SMSEvidence{Ready: true}, nil
	}
}

func (session *Session) smsCapabilityReady() bool {
	if session.smsContactConfirmed {
		return true
	}
	profile := vowifi.ResolveCarrierProfile(session.request.Identity)
	if profile.AllowSMSWithoutContactConfirmation {
		if !session.smsFallbackLogged {
			session.smsFallbackLogged = true
			if session.provider != nil && session.provider.config.Logger != nil {
				session.provider.config.Logger.Info("IMS SMS capability fallback enabled",
					"device_id", session.request.DeviceID,
					"carrier_profile", profile.ID,
					"match_source", profile.MatchSource,
					"reason", "registrar_contact_feature_not_confirmed")
			}
		}
		return true
	}
	return false
}

func (session *Session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.refreshCancel()
	select {
	case <-session.refreshDone:
	case <-ctx.Done():
		_ = session.closeOutboundFlow()
		<-session.refreshDone
	}

	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.registrationMu.Lock()
	var unregisterErr error
	if session.evidence.Registered && ctx.Err() == nil {
		response, err := session.register(ctx, 0)
		if err != nil {
			unregisterErr = err
		} else if response.StatusCode != 200 {
			unregisterErr = fmt.Errorf("ims: SIP deregistration returned %d", response.StatusCode)
		}
	}
	session.closed = true
	session.evidence.Registered = false
	session.evidence.RegistrationState = "closed"
	session.smsContactConfirmed = false
	session.clearAuthentication()
	session.registrationMu.Unlock()
	session.mu.Unlock()
	session.callMu.Lock()
	for _, call := range session.calls {
		if call.media != nil {
			_ = call.media.Close()
		}
	}
	session.callMu.Unlock()
	var cleanupErrors []error
	if unregisterErr != nil {
		cleanupErrors = append(cleanupErrors, unregisterErr)
	}
	// Runtime receive loops block in Read/Accept. Close every socket before
	// waiting for those goroutines; waiting first deadlocks VoWiFi shutdown and
	// leaves the modem permanently in CFUN=4.
	if err := session.closeOutboundFlow(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if session.inboundTCPListener != nil {
		if err := session.inboundTCPListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if session.inboundUDP != nil {
		if err := session.inboundUDP.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	session.closeInboundTCPConnections()
	session.receiveDone.Wait()
	if session.ipsecHandle != nil || session.clientIPSecHandle != nil || session.serverIPSecHandle != nil {
		// XFRM teardown is local and must still run when SIP deregistration has
		// consumed the caller's deadline. Use a fresh bounded context so a
		// service restart or Profile switch cannot strand the previous SIM's
		// transport-mode policies in the kernel.
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := session.closeProtectedSecurity(cleanupContext)
		cleanupCancel()
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func cloneEvidence(evidence vowifi.IMSEvidence) vowifi.IMSEvidence {
	evidence.PAssociatedURI = append([]string(nil), evidence.PAssociatedURI...)
	evidence.AssociatedIdentities = append([]string(nil), evidence.AssociatedIdentities...)
	evidence.ServiceRoute = append([]string(nil), evidence.ServiceRoute...)
	return evidence
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("ims: create random SIP identifier: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func randomUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("ims: create SIP instance identifier: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%x-%x-%x-%x-%x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16],
	), nil
}

func addressHost(address net.Addr) string {
	if address == nil {
		return "localhost"
	}
	host, _, err := net.SplitHostPort(address.String())
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(address.String(), "[]")
}

func addressIP(address net.Addr) net.IP {
	host := addressHost(address)
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return nil
	}
	return append(net.IP(nil), ip...)
}

func digitsBetween(value string, minimum int, maximum int) bool {
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

var _ vowifi.IMSProvider = (*Provider)(nil)
var _ vowifi.IMSSession = (*Session)(nil)
var _ vowifi.RuntimeFailureNotifier = (*Session)(nil)
var _ vowifi.IMSRuntimeStateNotifier = (*Session)(nil)
