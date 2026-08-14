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
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

const (
	defaultSIPPort              = 5060
	defaultRegistrationExpiry   = 3600 * time.Second
	defaultTransactionTimeout   = 12 * time.Second
	maxAuthenticationChallenges = 3
)

var (
	ErrRegistrationRejected      = errors.New("ims: SIP registration was rejected")
	ErrSessionClosed             = errors.New("ims: session is closed")
	ErrRegistrationExpired       = errors.New("ims: registration has expired")
	ErrSMSCapabilityNotConfirmed = errors.New("ims: registrar did not confirm the +g.3gpp.smsip contact")
)

// Config defines only deployment-specific SIP details. If PCSCF or
// LocalAddress is empty, Provider uses the corresponding value proven by the
// TunnelSession. The default transport is TCP and the default port is 5060.
type Config struct {
	PCSCF              string
	LocalAddress       string
	Transport          string
	TransportByPLMN    map[string]string
	Port               int
	RegistrationExpiry time.Duration
	TransactionTimeout time.Duration
	PrivateIdentity    string
	PublicIdentity     string
	// RequireExplicitIdentities disables the standards-based IMSI fallback.
	// Enable it when the device has no trusted ISIM reader and the carrier
	// profile must provide both IMPI and IMPU explicitly.
	RequireExplicitIdentities bool
	UserAgent                 string
	SecurityMode              SecurityMode
	IPSecInstaller            IPSecSAInstaller
	ProtectedClientPort       int
	ProtectedServerPort       int
	// SMSCenter is an operator-provided fallback when the SIM leaves EF_SMSP
	// and AT+CSCA empty. It must be an international or national digit string.
	SMSCenter string
	// Resolve refreshes deployment-specific IMS settings at the next session
	// boundary.  Existing registrations keep their original config until they
	// are torn down, which makes a config/profile update safe to reconnect.
	Resolve func(context.Context, vowifi.SIMIdentity) (Config, error)
	// OnSMS is invoked after a valid inbound RP-DATA/SMS-DELIVER has been
	// decoded. Returning an error causes an RP-ERROR delivery report.
	OnSMS func(context.Context, ReceivedSMS) error
	// OnSMSStatus is invoked for an SMS-STATUS-REPORT received after a
	// submission that requested a delivery report.
	OnSMSStatus func(context.Context, ReceivedSMSStatus) error
}

// Provider implements vowifi.IMSProvider using a small RFC 3261 REGISTER
// transaction and 3GPP AKAv1-MD5 authentication. It has no SIP stack or
// runtime dependency outside the Go standard library.
type Provider struct {
	aka       vowifi.AKAProvider
	config    Config
	installer IPSecSAInstaller
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
	return &Provider{aka: aka, config: normalized, installer: installer}, nil
}

func normalizeConfig(config Config) (Config, error) {
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
	if strings.TrimSpace(config.UserAgent) == "" {
		config.UserAgent = "vocat/1"
	}
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
	if config.SMSCenter != "" {
		digits := strings.TrimPrefix(config.SMSCenter, "+")
		if !digitsBetween(digits, 3, 20) {
			return Config{}, errors.New("ims: configured SMS service-centre address is invalid")
		}
	}
	return config, nil
}

func (provider *Provider) Start(ctx context.Context, request vowifi.IMSRequest) (vowifi.IMSSession, error) {
	if provider == nil {
		return nil, errors.New("ims: nil provider")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if provider.config.Resolve != nil {
		resolved, err := provider.config.Resolve(ctx, request.Identity)
		if err != nil {
			return nil, fmt.Errorf("ims: resolve carrier configuration: %w", err)
		}
		if resolved.PCSCF == "" {
			resolved.PCSCF = provider.config.PCSCF
		}
		if resolved.LocalAddress == "" {
			resolved.LocalAddress = provider.config.LocalAddress
		}
		if resolved.Port == 0 {
			resolved.Port = provider.config.Port
		}
		if resolved.RegistrationExpiry == 0 {
			resolved.RegistrationExpiry = provider.config.RegistrationExpiry
		}
		if resolved.TransactionTimeout == 0 {
			resolved.TransactionTimeout = provider.config.TransactionTimeout
		}
		if resolved.UserAgent == "" {
			resolved.UserAgent = provider.config.UserAgent
		}
		if resolved.SecurityMode == "" {
			resolved.SecurityMode = provider.config.SecurityMode
		}
		if resolved.IPSecInstaller == nil {
			resolved.IPSecInstaller = provider.config.IPSecInstaller
		}
		if resolved.ProtectedClientPort == 0 {
			resolved.ProtectedClientPort = provider.config.ProtectedClientPort
		}
		if resolved.ProtectedServerPort == 0 {
			resolved.ProtectedServerPort = provider.config.ProtectedServerPort
		}
		if resolved.OnSMS == nil {
			resolved.OnSMS = provider.config.OnSMS
		}
		if resolved.OnSMSStatus == nil {
			resolved.OnSMSStatus = provider.config.OnSMSStatus
		}
		resolved.Resolve = provider.config.Resolve
		normalized, err := normalizeConfig(resolved)
		if err != nil {
			return nil, err
		}
		copy := *provider
		copy.config = normalized
		provider = &copy
	}
	if request.Tunnel == nil {
		return nil, errors.New("ims: tunnel session is required")
	}
	tunnel := request.Tunnel.Evidence()
	if !tunnel.Established {
		return nil, vowifi.ErrTunnelNotEstablished
	}
	effectiveConfig := provider.config
	if effectiveConfig.Transport == "" && request.Carrier.IMSTransport != "" {
		effectiveConfig.Transport = request.Carrier.IMSTransport
	}
	identities, err := deriveIdentitiesWithProfile(request.Identity, effectiveConfig, request.Carrier)
	if err != nil {
		return nil, err
	}
	pcscf := provider.config.PCSCF
	if pcscf == "" {
		for _, candidate := range tunnel.PCSCF {
			if strings.TrimSpace(candidate) != "" {
				pcscf = candidate
				break
			}
		}
	}
	if pcscf == "" {
		return nil, errors.New("ims: tunnel did not provide a P-CSCF")
	}
	endpoint, transportHint, err := parsePCSCF(pcscf, provider.config.Port)
	if err != nil {
		return nil, err
	}
	if provider.config.PCSCF != "" && !pcscfProvenByTunnel(endpoint, tunnel.PCSCF, provider.config.Port) {
		return nil, errors.New("ims: configured P-CSCF is not proven by the SWu tunnel")
	}
	transport := transportForIdentity(effectiveConfig, request.Identity)
	if transport == "" {
		transport = transportHint
	}
	if transport == "" {
		transport = "tcp"
	}
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

	connection, err := dialSIP(ctx, transport, localAddress, 0, endpoint.address())
	if err != nil {
		return nil, fmt.Errorf("ims: connect to P-CSCF: %w", err)
	}
	session, err := newSession(provider, request, identities, endpoint, transport, connection)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if err := session.establish(ctx); err != nil {
		session.abort()
		return nil, err
	}
	return session, nil
}

func transportForIdentity(config Config, identity vowifi.SIMIdentity) string {
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimSpace(identity.HomeMNC)
	if transport := config.TransportByPLMN[mcc+mnc]; transport != "" {
		return transport
	}
	// A two-digit MNC and its three-digit zero-padded representation are
	// equivalent PLMNs; accept either spelling in deployment overrides.
	if len(mnc) == 2 {
		if transport := config.TransportByPLMN[mcc+"0"+mnc]; transport != "" {
			return transport
		}
	}
	if len(mnc) == 3 && strings.HasPrefix(mnc, "0") {
		if transport := config.TransportByPLMN[mcc+strings.TrimPrefix(mnc, "0")]; transport != "" {
			return transport
		}
	}
	return config.Transport
}

func usesO2GermanyIMSProfile(identity vowifi.SIMIdentity) bool {
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimLeft(strings.TrimSpace(identity.HomeMNC), "0")
	return mcc+mnc == "2623"
}

type identitySet struct {
	domain  string
	private string
	public  string
	user    string
	source  string
}

func deriveIdentities(identity vowifi.SIMIdentity, config Config) (identitySet, error) {
	return deriveIdentitiesWithProfile(identity, config, vowifi.CarrierProfile{})
}

func deriveIdentitiesWithProfile(
	identity vowifi.SIMIdentity,
	config Config,
	profile vowifi.CarrierProfile,
) (identitySet, error) {
	imsi := strings.TrimSpace(identity.IMSI)
	if !digitsBetween(imsi, 5, 16) {
		return identitySet{}, errors.New("ims: SIM IMSI is unavailable or invalid")
	}
	mcc := strings.TrimSpace(identity.HomeMCC)
	mnc := strings.TrimSpace(identity.HomeMNC)
	if !digitsBetween(mcc, 3, 3) || !digitsBetween(mnc, 2, 3) {
		return identitySet{}, errors.New("ims: home PLMN is unavailable or invalid")
	}
	for len(mnc) < 3 {
		mnc = "0" + mnc
	}
	domain := fmt.Sprintf("ims.mnc%s.mcc%s.3gppnetwork.org", mnc, mcc)
	privateIdentity := strings.TrimSpace(config.PrivateIdentity)
	publicIdentity := strings.TrimSpace(config.PublicIdentity)
	if (privateIdentity == "") != (publicIdentity == "") {
		return identitySet{}, errors.New("ims: private and public identities must be configured together")
	}
	if privateIdentity == "" {
		if config.RequireExplicitIdentities {
			return identitySet{}, errors.New("ims: explicit private and public identities are required when IMSI derivation is disabled")
		}
		privateIdentity = imsi + "@" + domain
		publicIdentity = "sip:" + imsi + "@" + domain
	} else {
		at := strings.LastIndexByte(privateIdentity, '@')
		if at <= 0 || at == len(privateIdentity)-1 {
			return identitySet{}, errors.New("ims: configured private identity is invalid")
		}
		domain = privateIdentity[at+1:]
		if strings.ContainsAny(domain, "<>\" \t;:@/") {
			return identitySet{}, errors.New("ims: configured private identity realm is invalid")
		}
	}
	source := profile.IMSIdentitySource
	if source == "" {
		source = vowifi.IMSIdentityExplicit
	}
	if strings.TrimSpace(config.PrivateIdentity) == "" {
		source = vowifi.IMSIdentityDerived
	}
	publicIdentityLower := strings.ToLower(publicIdentity)
	if strings.ContainsAny(privateIdentity+publicIdentity, "\r\n") ||
		!strings.Contains(privateIdentity, "@") ||
		(!strings.HasPrefix(publicIdentityLower, "sip:") &&
			!strings.HasPrefix(publicIdentityLower, "sips:")) {
		return identitySet{}, errors.New("ims: configured IMS identity is invalid")
	}
	user := publicIdentity[4:]
	if strings.HasPrefix(publicIdentityLower, "sips:") {
		user = publicIdentity[5:]
	}
	if at := strings.IndexByte(user, '@'); at >= 0 {
		user = user[:at]
	}
	if user == "" || strings.ContainsAny(user, "<>\" \t;") {
		return identitySet{}, errors.New("ims: public identity user is invalid")
	}
	return identitySet{domain: domain, private: privateIdentity, public: publicIdentity, user: user, source: source}, nil
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

type Session struct {
	provider         *Provider
	request          vowifi.IMSRequest
	identity         identitySet
	endpoint         pcscfEndpoint
	transport        string
	conn             net.Conn
	reader           *bufio.Reader
	initialEndpoint  pcscfEndpoint
	securityDeclined bool

	callID             string
	fromTag            string
	instanceID         string
	cseq               uint32
	auth               *authenticationState
	securityProposal   securityProposal
	securityAgreement  securityAgreement
	securityActive     bool
	ipsecHandle        IPSecSAHandle
	protectedTCP       *net.TCPListener
	protectedUDP       *net.UDPConn
	failures           chan error
	failureOnce        sync.Once
	writeMu            sync.Mutex
	transactionsMu     sync.Mutex
	transactions       map[sipTransactionKey]chan *sipResponse
	runtimeStarted     bool
	receiveDone        sync.WaitGroup
	inboundMu          sync.Mutex
	inboundConnections map[net.Conn]struct{}
	smsMu              sync.Mutex
	nextRPReference    byte
	smsTR1M            time.Duration
	smsSubmitMu        sync.Mutex
	smsSubmit          map[byte]*smsSubmitTransaction
	smsServerMu        sync.Mutex
	smsServer          map[smsServerTransactionKey]*smsServerTransaction
	callMu             sync.Mutex
	calls              map[string]*imsCall

	mu                  sync.Mutex
	closed              bool
	evidence            vowifi.IMSEvidence
	smsContactConfirmed bool
	expiresAt           time.Time
	refreshContext      context.Context
	refreshCancel       context.CancelFunc
	refreshDone         chan struct{}
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
	instanceID, err := sipInstanceUUID(request)
	if err != nil {
		return nil, err
	}
	refreshContext, refreshCancel := context.WithCancel(context.Background())
	session := &Session{
		provider:           provider,
		request:            request,
		identity:           identity,
		endpoint:           endpoint,
		initialEndpoint:    endpoint,
		transport:          transport,
		conn:               connection,
		callID:             callToken + "@" + addressHost(connection.LocalAddr()),
		fromTag:            fromTag,
		instanceID:         "urn:uuid:" + instanceID,
		cseq:               1,
		refreshContext:     refreshContext,
		refreshCancel:      refreshCancel,
		refreshDone:        make(chan struct{}),
		failures:           make(chan error, 1),
		transactions:       make(map[sipTransactionKey]chan *sipResponse),
		inboundConnections: make(map[net.Conn]struct{}),
		smsSubmit:          make(map[byte]*smsSubmitTransaction),
		smsServer:          make(map[smsServerTransactionKey]*smsServerTransaction),
		calls:              make(map[string]*imsCall),
		evidence: vowifi.IMSEvidence{
			RegistrationState: "registering",
			Transport:         transport,
			IdentitySource:    identity.source,
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
		proposal, err := newSecurityProposal(
			localIP,
			provider.config.ProtectedClientPort,
			provider.config.ProtectedServerPort,
		)
		if err != nil {
			refreshCancel()
			return nil, err
		}
		session.securityProposal = proposal
		protectedTCP, err := net.ListenTCP(
			"tcp",
			&net.TCPAddr{IP: append(net.IP(nil), localIP...), Port: proposal.portServer},
		)
		if err != nil {
			refreshCancel()
			return nil, fmt.Errorf("ims: reserve protected TCP server port: %w", err)
		}
		protectedUDP, err := net.ListenUDP(
			"udp",
			&net.UDPAddr{IP: append(net.IP(nil), localIP...), Port: proposal.portServer},
		)
		if err != nil {
			_ = protectedTCP.Close()
			refreshCancel()
			return nil, fmt.Errorf("ims: reserve protected UDP server port: %w", err)
		}
		session.protectedTCP = protectedTCP
		session.protectedUDP = protectedUDP
	}
	return session, nil
}

func (session *Session) abort() {
	session.refreshCancel()
	_ = session.conn.Close()
	if session.protectedTCP != nil {
		_ = session.protectedTCP.Close()
	}
	if session.protectedUDP != nil {
		_ = session.protectedUDP.Close()
	}
	if session.ipsecHandle != nil {
		_ = session.ipsecHandle.Close(context.Background())
	}
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
	if err := session.applyRegistrationEvidence(response); err != nil {
		return err
	}
	if err := session.startRuntimeReceivers(); err != nil {
		return err
	}
	go session.refreshLoop()
	return nil
}

// registrationRejectionError preserves the registrar's safe diagnostic text.
// Operators commonly use the SIP reason phrase, Reason, or Warning header to
// distinguish an unprovisioned subscriber from a malformed REGISTER. Do not
// include authentication headers here because they contain AKA material.
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
	return fmt.Errorf("%w: %s", ErrRegistrationRejected, message)
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
	for challenges := 0; challenges <= maxAuthenticationChallenges; challenges++ {
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
		request, err := session.buildRegister(cseq, expires, authorizationHeader, authorization)
		if err != nil {
			return nil, err
		}
		response, err := session.exchange(ctx, request, cseq)
		if err != nil {
			return nil, err
		}
		session.evidence.LastSIPCode = response.StatusCode
		if response.StatusCode != 401 && response.StatusCode != 407 {
			return response, nil
		}
		if challenges == maxAuthenticationChallenges {
			break
		}
		if session.securityActive {
			return nil, errors.New("ims: protected registration was challenged again")
		}
		challenge, err := challengeFromResponse(response)
		if err != nil {
			return nil, err
		}
		agreement, useSecurity, err := session.securityFromResponse(response)
		if err != nil {
			return nil, err
		}
		material, err := authenticateAKA(ctx, session.provider.aka, session.request.Identity, challenge)
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
	branch, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	local := session.conn.LocalAddr().String()
	contactAddress := session.contactAddress()
	transportUpper := strings.ToUpper(session.transport)
	requestURI := "sip:" + session.identity.domain
	routeURI := "sip:" + session.endpoint.address() + ";transport=" + session.transport + ";lr"
	o2Germany := usesO2GermanyIMSProfile(session.request.Identity)
	supported := "path, sec-agree"
	allow := "REGISTER, MESSAGE, INVITE, ACK, CANCEL, BYE, OPTIONS"
	if o2Germany {
		supported = "path, gruu, outbound, sec-agree, 100rel, timer"
		allow = "INVITE, ACK, CANCEL, BYE, PRACK, UPDATE, INFO, MESSAGE, OPTIONS"
	}
	contact := fmt.Sprintf(
		"<sip:%s@%s;transport=%s>;+g.3gpp.accesstype=\"wlan1\";+sip.instance=\"<%s>\";"+
			"+g.3gpp.smsip;audio;"+
			`+g.3gpp.icsi-ref="%s"`,
		session.identity.user,
		contactAddress,
		session.transport,
		session.instanceID,
		"urn%3Aurn-7%3A3gpp-service.ims.icsi.mmtel,urn%3Aurn-7%3A3gpp-service.ims.icsi.oma.cpm.msg,urn%3Aurn-7%3A3gpp-service.ims.icsi.oma.cpm.sms",
	)
	lines := []string{
		"REGISTER " + requestURI + " SIP/2.0",
		fmt.Sprintf("Via: SIP/2.0/%s %s;branch=z9hG4bK%s;rport", transportUpper, local, branch),
		"Max-Forwards: 70",
		"Route: <" + routeURI + ">",
		"From: <" + session.identity.public + ">;tag=" + session.fromTag,
		"To: <" + session.identity.public + ">",
		"Call-ID: " + session.callID,
		fmt.Sprintf("CSeq: %d REGISTER", cseq),
		"Contact: " + contact,
		fmt.Sprintf("Expires: %d", expires),
		"Supported: " + supported,
		"Allow: " + allow,
		"User-Agent: " + session.provider.config.UserAgent,
	}
	if o2Germany {
		lines = append(lines, "P-Preferred-Identity: <"+session.identity.public+">")
	}
	if session.securityOffered() {
		lines = append(
			lines,
			"Security-Client: "+session.securityProposal.headerValue(),
			"Require: sec-agree",
			"Proxy-Require: sec-agree",
		)
		if session.securityActive {
			lines = append(lines, "Security-Verify: "+session.securityAgreement.verifyValue)
		}
	}
	if authorization != "" {
		if session.securityOffered() {
			integrity := "no"
			if session.securityActive {
				integrity = "yes"
			}
			authorization += ", integrity-protected=" + integrity
		}
		lines = append(lines, authorizationHeader+": "+authorization)
	} else if cseq == 1 && session.securityOffered() {
		lines = append(lines, "Authorization: "+session.emptyDigestAuthorization())
	}
	lines = append(lines, "Content-Length: 0", "", "")
	return []byte(strings.Join(lines, "\r\n")), nil
}

func (session *Session) exchange(ctx context.Context, request []byte, cseq uint32) (*sipResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.runtimeStarted {
		return session.exchangeRuntime(ctx, request, sipTransactionKey{
			callID: session.callID,
			cseq:   cseq,
			method: "REGISTER",
		})
	}
	deadline := time.Now().Add(session.provider.config.TransactionTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	readUDP := session.protectedUDP
	protectedUDP := session.securityActive && session.transport == "udp" && readUDP != nil
	if err := session.conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("ims: set SIP transaction deadline: %w", err)
	}
	if protectedUDP {
		if err := readUDP.SetReadDeadline(deadline); err != nil {
			return nil, fmt.Errorf("ims: set protected SIP receive deadline: %w", err)
		}
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = session.conn.SetDeadline(time.Now())
		if protectedUDP {
			_ = readUDP.SetReadDeadline(time.Now())
		}
	})
	defer stopCancellation()
	if _, err := session.conn.Write(request); err != nil {
		return nil, fmt.Errorf("ims: send SIP REGISTER: %w", err)
	}

	for {
		var response *sipResponse
		var err error
		if session.transport == "tcp" {
			response, err = readSIPResponse(session.reader)
		} else if protectedUDP {
			packet := make([]byte, 65535)
			var count int
			var remote *net.UDPAddr
			count, remote, err = readUDP.ReadFromUDP(packet)
			if err == nil && !session.validProtectedUDPSource(remote) {
				continue
			}
			if err == nil {
				response, err = parseSIPResponse(packet[:count])
			}
		} else {
			packet := make([]byte, 65535)
			var count int
			count, err = session.conn.Read(packet)
			if err == nil {
				response, err = parseSIPResponse(packet[:count])
			}
		}
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, contextErr
			}
			return nil, fmt.Errorf("ims: receive SIP REGISTER response: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(response.value("Call-ID")), session.callID) {
			continue
		}
		responseCSeq, method, err := cseqNumber(response.value("CSeq"))
		if err != nil || responseCSeq != cseq || method != "REGISTER" {
			continue
		}
		if response.StatusCode >= 100 && response.StatusCode < 200 {
			continue
		}
		return response, nil
	}
}

func (session *Session) applyRegistrationEvidence(response *sipResponse) error {
	if session.provider.config.SecurityMode == SecurityRequired && !session.securityActive {
		session.evidence.Registered = false
		session.evidence.RegistrationState = "security_failed"
		return ErrIPSecAgreementRequired
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
	expiry := registrationExpiry(response, registeredContact, session.provider.config.RegistrationExpiry)
	if expiry <= 0 {
		session.evidence.Registered = false
		session.evidence.RegistrationState = "rejected_zero_expiry"
		session.evidence.LastSIPCode = response.StatusCode
		session.clearAuthentication()
		return fmt.Errorf("%w: registrar granted zero expiry", ErrRegistrationRejected)
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
		IdentitySource:       session.identity.source,
		LastSIPCode:          response.StatusCode,
		SecurityMode:         session.effectiveSecurityMode(),
		SecurityVerified:     session.securityActive,
	}
	session.clearAuthentication()
	return nil
}

func (session *Session) refreshLoop() {
	defer close(session.refreshDone)
	for {
		session.mu.Lock()
		if session.closed || !session.evidence.Registered {
			session.mu.Unlock()
			return
		}
		delay := refreshDelay(time.Until(session.expiresAt))
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
		if err := session.refreshOnce(session.refreshContext); err != nil {
			session.publishFailure(err)
			return
		}
	}
}

func refreshDelay(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 0
	}
	delay := remaining * 4 / 5
	if remaining > time.Minute && delay > remaining-30*time.Second {
		delay = remaining - 30*time.Second
	}
	if delay < 100*time.Millisecond {
		delay = 100 * time.Millisecond
	}
	return delay
}

func (session *Session) refreshOnce(ctx context.Context) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	switch {
	case session.closed:
		return ErrSessionClosed
	case !session.evidence.Registered:
		return vowifi.ErrIMSNotRegistered
	}
	response, err := session.register(ctx, int(session.provider.config.RegistrationExpiry/time.Second))
	if err != nil {
		session.failRefresh()
		return fmt.Errorf("ims: refresh registration: %w", err)
	}
	if response.StatusCode != 200 {
		session.failRefresh()
		return fmt.Errorf("ims: refresh registration returned SIP %d", response.StatusCode)
	}
	if err := session.applyRegistrationEvidence(response); err != nil {
		session.failRefresh()
		return err
	}
	return nil
}

func (session *Session) failRefresh() {
	session.evidence.Registered = false
	session.evidence.RegistrationState = "refresh_failed"
	session.smsContactConfirmed = false
	session.expiresAt = time.Time{}
	session.clearAuthentication()
}

func (session *Session) publishFailure(err error) {
	if err == nil {
		return
	}
	session.failureOnce.Do(func() {
		session.failures <- err
	})
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

func registrationExpiry(response *sipResponse, registeredContact string, fallback time.Duration) time.Duration {
	if seconds, ok := parameterSeconds(registeredContact, "expires"); ok {
		return boundedExpiry(seconds)
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(response.value("Expires"))); err == nil {
		return boundedExpiry(seconds)
	}
	return fallback
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
	// A registrar-confirmed feature tag proves the SIP capability. Sending also
	// needs a usable TS-SCA, but probing it must happen outside session.mu because
	// a SIM/modem reader may block on I/O.
	switch {
	case session.closed:
		session.mu.Unlock()
		return vowifi.SMSEvidence{}, ErrSessionClosed
	case !session.evidence.Registered:
		session.mu.Unlock()
		return vowifi.SMSEvidence{}, vowifi.ErrIMSNotRegistered
	case !session.expiresAt.IsZero() && !time.Now().Before(session.expiresAt):
		session.mu.Unlock()
		return vowifi.SMSEvidence{}, ErrRegistrationExpired
	case !session.smsContactConfirmed:
		session.mu.Unlock()
		return vowifi.SMSEvidence{Ready: false}, ErrSMSCapabilityNotConfirmed
	}
	smsc := strings.TrimSpace(session.request.Identity.SMSC)
	deviceID := session.request.DeviceID
	session.mu.Unlock()
	if smsc != "" {
		if _, err := encodeRPAddress(smsc); err != nil {
			return vowifi.SMSEvidence{Ready: false}, ErrSMSCUnavailable
		}
		return vowifi.SMSEvidence{Ready: true}, nil
	}
	var readErr error
	if reader, ok := session.provider.aka.(smsCenterReader); ok {
		smsc, readErr = reader.ReadSMSCenter(ctx, deviceID)
	}
	if strings.TrimSpace(smsc) == "" {
		smsc = session.provider.config.SMSCenter
	}
	if strings.TrimSpace(smsc) == "" {
		return vowifi.SMSEvidence{Ready: false}, errors.Join(ErrSMSCUnavailable, readErr)
	}
	if _, err := encodeRPAddress(smsc); err != nil {
		return vowifi.SMSEvidence{Ready: false}, errors.Join(ErrSMSCUnavailable, readErr)
	}
	// Do not cache a value read from the modem here: a later SIM swap must be
	// observed by the next readiness/send probe rather than inheriting stale TS-SCA.
	return vowifi.SMSEvidence{Ready: true}, nil
}

func (session *Session) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	session.refreshCancel()
	select {
	case <-session.refreshDone:
	case <-ctx.Done():
		_ = session.conn.Close()
		<-session.refreshDone
	}

	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
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
	session.mu.Unlock()
	session.callMu.Lock()
	for _, call := range session.calls {
		if call.reliableCancel != nil {
			call.reliableCancel()
			call.reliableCancel = nil
			call.reliableGeneration++
		}
		if call.finalCancel != nil {
			call.finalCancel()
			call.finalCancel = nil
			call.finalGeneration++
		}
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
	if err := session.conn.Close(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if session.protectedTCP != nil {
		if err := session.protectedTCP.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if session.protectedUDP != nil {
		if err := session.protectedUDP.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	session.closeInboundConnections()
	session.receiveDone.Wait()
	if session.ipsecHandle != nil {
		if err := session.ipsecHandle.Close(ctx); err != nil {
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

// sipInstanceUUID returns the RFC 5626 SIP instance identifier for this UE.
// A +sip.instance identifies the physical user agent and must survive process
// restarts. Generating a new UUID for every IMS retry makes strict registrars
// (including Telefonica/O2 deployments) see one modem as many simultaneous
// devices and can turn an otherwise valid initial REGISTER into SIP 403.
func sipInstanceUUID(request vowifi.IMSRequest) (string, error) {
	identifier := strings.TrimSpace(request.Identity.IMEI)
	identifierType := "imei"
	if identifier == "" {
		identifier = strings.ToLower(strings.TrimSpace(request.DeviceID))
		identifierType = "device"
	}
	if identifier == "" {
		return randomUUID()
	}

	// A private, fixed UUID namespace plus the equipment identity gives the
	// same RFC 4122 version-5 UUID after every reconnect without storing or
	// exposing the raw IMEI in SIP headers.
	namespace := [16]byte{
		0x8d, 0xd1, 0x93, 0x64, 0x8a, 0x6f, 0x4b, 0x29,
		0x9b, 0x64, 0x88, 0x7d, 0x61, 0x3e, 0x7a, 0x11,
	}
	hash := sha1.New() // SHA-1 is required by UUIDv5; this is not a security credential.
	_, _ = hash.Write(namespace[:])
	_, _ = hash.Write([]byte("vocat-sip-instance:" + identifierType + ":" + identifier))
	value := hash.Sum(nil)[:16]
	value[6] = (value[6] & 0x0f) | 0x50
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
