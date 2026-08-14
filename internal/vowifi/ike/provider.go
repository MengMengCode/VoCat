package ike

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

type Config struct {
	Random             io.Reader
	Resolver           *net.Resolver
	Dialer             *net.Dialer
	RootCAs            *x509.CertPool
	ResponderPublicKey crypto.PublicKey
	ServerName         string
	Timeout            time.Duration
	KeepaliveInterval  time.Duration
	Installer          ChildSAInstaller
	IdentityType       uint8
	APN                string
	// EAPMethod is an explicit anti-downgrade policy: "aka" (type 23) or
	// "aka-prime" (type 50). Automatic fallback is intentionally unsupported.
	EAPMethod string
	// AllowSHA1 advertises SHA-1 only as a lower-priority compatibility
	// alternative; SHA-256 remains preferred.
	AllowSHA1 bool
	// UseMODP1024 explicitly selects DH group 2 instead of the group 14 default.
	UseMODP1024 bool
	// Resolve refreshes deployment configuration before each new tunnel.  The
	// callback is intentionally evaluated only at session boundaries, so a
	// live tunnel is never mutated underneath the IKE state machine.
	Resolve func(context.Context, vowifi.SIMIdentity) (Config, error)
}

type Provider struct {
	config           Config
	transportFactory func(context.Context, transportConfig, vowifi.ProxyRoute, string) (datagramTransport, error)
}

func NewProvider(config Config) (*Provider, error) {
	normalized, err := normalizeProviderConfig(config)
	if err != nil {
		return nil, err
	}
	return &Provider{config: normalized, transportFactory: newDatagramTransport}, nil
}

func normalizeProviderConfig(config Config) (Config, error) {
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{}
	}
	if config.Timeout < 0 {
		return Config{}, errors.New("ike: timeout must not be negative")
	}
	if config.Timeout == 0 {
		config.Timeout = 12 * time.Second
	}
	if config.KeepaliveInterval < 0 {
		return Config{}, errors.New("ike: keepalive interval must not be negative")
	}
	if config.KeepaliveInterval == 0 {
		config.KeepaliveInterval = 20 * time.Second
	}
	if config.IdentityType == 0 {
		config.IdentityType = 3 // ID_RFC822_ADDR, carrying the permanent NAI.
	}
	config.APN = strings.TrimSpace(config.APN)
	if config.APN == "" {
		config.APN = "ims"
	}
	if len(config.APN) > 253 || strings.ContainsAny(config.APN, " \t\r\n/:@") {
		return Config{}, errors.New("ike: APN is invalid")
	}
	config.EAPMethod = strings.ToLower(strings.TrimSpace(config.EAPMethod))
	if config.EAPMethod == "" {
		config.EAPMethod = "aka"
	}
	if config.EAPMethod != "aka" && config.EAPMethod != "aka-prime" {
		return Config{}, fmt.Errorf("ike: unsupported EAP method %q", config.EAPMethod)
	}
	if config.Installer == nil {
		config.Installer = defaultChildSAInstaller()
	}
	return config, nil
}

func (provider *Provider) Start(ctx context.Context, request vowifi.TunnelRequest) (vowifi.TunnelSession, error) {
	if provider == nil {
		return nil, errors.New("ike: nil provider")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.AKA == nil {
		return nil, errors.New("ike: AKA provider is required")
	}
	if provider.config.Resolve != nil {
		resolved, err := provider.config.Resolve(ctx, request.Identity)
		if err != nil {
			return nil, fmt.Errorf("ike: resolve carrier configuration: %w", err)
		}
		if resolved.Random == nil {
			resolved.Random = provider.config.Random
		}
		if resolved.Resolver == nil {
			resolved.Resolver = provider.config.Resolver
		}
		if resolved.Dialer == nil {
			resolved.Dialer = provider.config.Dialer
		}
		if resolved.RootCAs == nil {
			resolved.RootCAs = provider.config.RootCAs
		}
		if resolved.ResponderPublicKey == nil {
			resolved.ResponderPublicKey = provider.config.ResponderPublicKey
		}
		if resolved.ServerName == "" {
			resolved.ServerName = provider.config.ServerName
		}
		if resolved.Installer == nil {
			resolved.Installer = provider.config.Installer
		}
		resolved.Resolve = provider.config.Resolve
		normalized, err := normalizeProviderConfig(resolved)
		if err != nil {
			return nil, err
		}
		copy := *provider
		copy.config = normalized
		provider = &copy
	}
	epdg := strings.TrimSpace(request.EPDG)
	if epdg == "" || strings.ContainsAny(epdg, " \t\r\n/:") {
		return nil, errors.New("ike: ePDG must be a hostname")
	}
	// Telefonica Germany's O2 ePDGs currently discard a SHA-256-only
	// IKE_SA_INIT instead of returning NO_PROPOSAL_CHOSEN. Advertise SHA-1 as a
	// lower-priority compatibility transform for those home PLMNs while keeping
	// MODP2048 and the strong-first ordering. Other carriers still require the
	// explicit AllowSHA1 setting before any legacy transform is advertised.
	allowSHA1 := provider.config.AllowSHA1 || request.Carrier.AllowSHA1 || telefonicaGermanySHA1Compatibility(request.Identity)
	eapMethod := provider.config.EAPMethod
	if eapMethod == "aka" && request.Carrier.EAPMethod != "" {
		eapMethod = request.Carrier.EAPMethod
	}
	aka, err := newAKAClientWithMethod(request.Identity, request.AKA, eapMethod)
	if err != nil {
		return nil, err
	}
	transport, err := provider.transportFactory(ctx, transportConfig{
		Resolver: provider.config.Resolver,
		Dialer:   provider.config.Dialer,
		Timeout:  provider.config.Timeout,
	}, request.Proxy, epdg)
	if err != nil {
		return nil, err
	}
	closeTransport := true
	defer func() {
		if closeTransport {
			_ = transport.Close()
		}
	}()

	group := uint16(dhMODP2048)
	if provider.config.UseMODP1024 {
		group = dhMODP1024
	}
	dh, err := newDHExchange(group, provider.config.Random)
	if err != nil {
		return nil, err
	}
	var initiatorSPI [8]byte
	if err := fillNonzero(provider.config.Random, initiatorSPI[:]); err != nil {
		return nil, err
	}
	initiatorNonce := make([]byte, 32)
	if _, err := io.ReadFull(provider.config.Random, initiatorNonce); err != nil {
		return nil, fmt.Errorf("ike: generate initiator nonce: %w", err)
	}
	ikeProposalBody, err := marshalProposals([]proposal{ikeOffer(group, allowSHA1)})
	if err != nil {
		return nil, err
	}
	keBody := make([]byte, 4+len(dh.Public))
	binary.BigEndian.PutUint16(keBody[0:2], group)
	copy(keBody[4:], dh.Public)
	localAddress := transport.LocalAddr()
	remoteAddress := transport.RemoteAddr()
	if localAddress == nil || remoteAddress == nil {
		return nil, errors.New("ike: transport did not expose UDP endpoints")
	}
	sourceHash, err := natDetectionHash(initiatorSPI, [8]byte{}, localAddress.IP, uint16(localAddress.Port))
	if err != nil {
		return nil, err
	}
	destinationHash, err := natDetectionHash(initiatorSPI, [8]byte{}, remoteAddress.IP, uint16(remoteAddress.Port))
	if err != nil {
		return nil, err
	}
	initPayloads := []payload{
		{Type: payloadSA, Body: ikeProposalBody},
		{Type: payloadKE, Body: keBody},
		{Type: payloadNonce, Body: initiatorNonce},
		makeNotify(notifyNATSource, sourceHash),
		makeNotify(notifyNATDestination, destinationHash),
	}
	first, initBody, err := marshalPayloadChain(initPayloads)
	if err != nil {
		return nil, err
	}
	initRequest := ikeHeader{
		InitiatorSPI: initiatorSPI,
		NextPayload:  first,
		Exchange:     exchangeIKEInit,
		Flags:        flagInitiator,
		MessageID:    0,
	}.marshal(initBody)
	initResponse, err := transport.RoundTrip(ctx, initRequest)
	if err != nil {
		return nil, err
	}
	responseHeader, responseBody, err := validateResponse(initResponse, initiatorSPI, [8]byte{}, exchangeIKEInit, 0)
	if err != nil {
		return nil, err
	}
	if responseHeader.ResponderSPI == [8]byte{} {
		return nil, errors.New("ike: responder returned a zero SPI")
	}
	initResponsePayloads, err := parsePayloadChain(responseHeader.NextPayload, responseBody)
	if err != nil {
		return nil, err
	}
	if err := rejectFatalNotifications(initResponsePayloads); err != nil {
		return nil, err
	}
	saPayload, err := onePayload(initResponsePayloads, payloadSA)
	if err != nil {
		return nil, err
	}
	selectedProposals, err := parseProposals(saPayload.Body)
	if err != nil || len(selectedProposals) != 1 {
		return nil, errors.New("ike: responder did not select exactly one IKE proposal")
	}
	ikeSuite, err := parseIKESuite(selectedProposals[0])
	if err != nil {
		return nil, err
	}
	if ikeSuite.DHID != group {
		return nil, fmt.Errorf("ike: responder selected DH group %d but KE used group %d", ikeSuite.DHID, group)
	}
	if !allowSHA1 &&
		(ikeSuite.PRFID != prfHMACSHA256 || ikeSuite.IntegrityID != integrityHMACSHA256_128) {
		return nil, errors.New("ike: responder selected legacy IKE crypto while legacy compatibility is disabled")
	}
	responderKE, err := onePayload(initResponsePayloads, payloadKE)
	if err != nil {
		return nil, err
	}
	if len(responderKE.Body) < 4 || binary.BigEndian.Uint16(responderKE.Body[0:2]) != group {
		return nil, errors.New("ike: responder KE group does not match the selected proposal")
	}
	sharedSecret, err := dh.shared(responderKE.Body[4:])
	if err != nil {
		return nil, err
	}
	responderNoncePayload, err := onePayload(initResponsePayloads, payloadNonce)
	if err != nil {
		return nil, err
	}
	if len(responderNoncePayload.Body) < 16 || len(responderNoncePayload.Body) > 256 {
		return nil, errors.New("ike: responder nonce length is outside 16..256 bytes")
	}
	responderNonce := responderNoncePayload.Body
	keys, err := deriveIKEKeys(
		ikeSuite,
		sharedSecret,
		initiatorNonce,
		responderNonce,
		initiatorSPI,
		responseHeader.ResponderSPI,
	)
	if err != nil {
		return nil, err
	}
	natDetected, err := detectNAT(
		initResponsePayloads,
		initiatorSPI,
		responseHeader.ResponderSPI,
		transport.LocalAddr(),
		transport.RemoteAddr(),
	)
	if err != nil {
		return nil, err
	}
	if request.Proxy.Mode == vowifi.ProxyModeSOCKS5 {
		natDetected = true
	}
	if natDetected {
		if err := transport.Float(ctx); err != nil {
			return nil, err
		}
	}

	var childInboundSPIBytes [4]byte
	if err := fillNonzero(provider.config.Random, childInboundSPIBytes[:]); err != nil {
		return nil, err
	}
	childInboundSPI := binary.BigEndian.Uint32(childInboundSPIBytes[:])
	childOfferBody, err := marshalProposals([]proposal{espOffer(childInboundSPIBytes[:], allowSHA1)})
	if err != nil {
		return nil, err
	}
	identityType := provider.config.IdentityType
	if request.Carrier.IKEIdentityType != 0 {
		identityType = request.Carrier.IKEIdentityType
	}
	idi := payload{Type: payloadIDi, Body: append([]byte{identityType, 0, 0, 0}, aka.identity...)}
	apn := provider.config.APN
	if apn == "ims" && request.Carrier.IMSAPN != "" {
		apn = request.Carrier.IMSAPN
	}
	requestedIDr := payload{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte(apn)...)}
	tsi := dualStackTrafficSelectors(payloadTSi)
	tsr := dualStackTrafficSelectors(payloadTSr)
	firstAuthPayloads := buildInitialEAPOnlyAuth(idi, requestedIDr, childOfferBody, tsi, tsr)
	authHeader := ikeHeader{
		InitiatorSPI: initiatorSPI,
		ResponderSPI: responseHeader.ResponderSPI,
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    1,
	}
	authRequest, err := encryptPayloads(authHeader, firstAuthPayloads, ikeSuite, keys.SKei, keys.SKai, provider.config.Random)
	if err != nil {
		return nil, err
	}
	authResponse, err := transport.RoundTrip(ctx, authRequest)
	if err != nil {
		return nil, err
	}
	authResponseHeader, authResponsePayloads, err := decryptAndValidate(
		authResponse, initiatorSPI, responseHeader.ResponderSPI, exchangeIKEAuth, 1, ikeSuite, keys,
	)
	if err != nil {
		return nil, err
	}
	_ = authResponseHeader
	serverName := strings.TrimSpace(provider.config.ServerName)
	if serverName == "" {
		serverName = epdg
	}
	responderAUTH, responderID, err := validateInitialResponderAUTH(
		authResponsePayloads,
		initResponse,
		initiatorNonce,
		ikeSuite,
		keys.SKpr,
		serverName,
		serverName,
		provider.config.RootCAs,
		provider.config.ResponderPublicKey,
		true, // RFC 5998 EAP-only authentication defers responder AUTH.
	)
	if err != nil {
		return nil, err
	}
	messageID := uint32(1)
	currentPayloads := authResponsePayloads
	for round := 0; round < 10; round++ {
		eapPayload, err := onePayload(currentPayloads, payloadEAP)
		if err != nil {
			return nil, fmt.Errorf("ike: IKE_AUTH EAP round %d: %w", round+1, err)
		}
		action, err := aka.handle(ctx, eapPayload.Body)
		if err != nil {
			return nil, err
		}
		if action.Success {
			break
		}
		if len(action.Response) == 0 {
			if packet, parseErr := parseEAPPacket(eapPayload.Body); parseErr == nil {
				return nil, fmt.Errorf(
					"ike: EAP state machine produced no response: code=%d identifier=%d type=%d method_started=%t challenge_complete=%t failure_expected=%t",
					packet.Code, packet.Identifier, packet.Type,
					aka.methodStarted, aka.challengeComplete, aka.failureExpected,
				)
			}
			return nil, errors.New("ike: EAP state machine produced no response")
		}
		messageID++
		eapRequest, err := encryptPayloads(ikeHeader{
			InitiatorSPI: initiatorSPI,
			ResponderSPI: responseHeader.ResponderSPI,
			Exchange:     exchangeIKEAuth,
			Flags:        flagInitiator,
			MessageID:    messageID,
		}, []payload{{Type: payloadEAP, Body: action.Response}}, ikeSuite, keys.SKei, keys.SKai, provider.config.Random)
		if err != nil {
			return nil, err
		}
		eapResponse, err := transport.RoundTrip(ctx, eapRequest)
		if err != nil {
			return nil, err
		}
		_, currentPayloads, err = decryptAndValidate(
			eapResponse, initiatorSPI, responseHeader.ResponderSPI, exchangeIKEAuth, messageID, ikeSuite, keys,
		)
		if err != nil {
			return nil, err
		}
		if round == 9 {
			return nil, errors.New("ike: EAP exchange exceeded ten IKE_AUTH rounds")
		}
	}
	if !aka.challengeComplete || len(aka.keys.MSK) != 64 {
		return nil, errors.New("ike: EAP-AKA did not produce an authenticated MSK")
	}
	initiatorAUTH, err := makeEAPInitiatorAUTH(
		aka.keys.MSK,
		initRequest,
		responderNonce,
		ikeSuite,
		keys.SKpi,
		idi,
	)
	if err != nil {
		return nil, err
	}
	messageID++
	finalRequest, err := encryptPayloads(ikeHeader{
		InitiatorSPI: initiatorSPI,
		ResponderSPI: responseHeader.ResponderSPI,
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    messageID,
	}, []payload{initiatorAUTH}, ikeSuite, keys.SKei, keys.SKai, provider.config.Random)
	if err != nil {
		return nil, err
	}
	finalResponse, err := transport.RoundTrip(ctx, finalRequest)
	if err != nil {
		return nil, err
	}
	_, finalPayloads, err := decryptAndValidate(
		finalResponse, initiatorSPI, responseHeader.ResponderSPI, exchangeIKEAuth, messageID, ikeSuite, keys,
	)
	if err != nil {
		return nil, err
	}
	if err := rejectFatalNotifications(finalPayloads); err != nil {
		return nil, err
	}
	finalAUTHs := payloadsOfType(finalPayloads, payloadAuth)
	if len(finalAUTHs) != 1 {
		return nil, fmt.Errorf("%w: final EAP-only response must contain exactly one MSK AUTH payload", vowifi.ErrResponderAUTHRequired)
	}
	if len(responderID.Body) == 0 {
		return nil, errors.New("ike: EAP-only exchange has no initial ePDG IDr for the responder AUTH transcript")
	}
	finalIDs := payloadsOfType(finalPayloads, payloadIDr)
	if len(finalIDs) > 1 {
		return nil, errors.New("ike: duplicate final responder IDr payload")
	}
	if len(finalIDs) == 1 {
		if err := validateFQDNIDr(finalIDs[0], apn, "final APN"); err != nil {
			return nil, fmt.Errorf("ike: final APN IDr: %w", err)
		}
	}
	if err := verifyEAPResponderAUTH(
		finalAUTHs[0],
		aka.keys.MSK,
		initResponse,
		initiatorNonce,
		ikeSuite,
		keys.SKpr,
		responderID,
	); err != nil {
		return nil, err
	}
	responderAUTH = vowifi.ResponderAUTHVerified

	childSA, err := onePayload(finalPayloads, payloadSA)
	if err != nil {
		return nil, err
	}
	childProposals, err := parseProposals(childSA.Body)
	if err != nil || len(childProposals) != 1 {
		return nil, errors.New("ike: responder did not select exactly one ESP proposal")
	}
	childSuite, err := parseESPSuite(childProposals[0])
	if err != nil {
		return nil, err
	}
	if !allowSHA1 && childSuite.IntegrityID != integrityHMACSHA256_128 {
		return nil, errors.New("ike: responder selected legacy ESP integrity while legacy compatibility is disabled")
	}
	childOutboundSPI := binary.BigEndian.Uint32(childProposals[0].SPI)
	finalTSi, err := onePayload(finalPayloads, payloadTSi)
	if err != nil {
		return nil, err
	}
	finalTSr, err := onePayload(finalPayloads, payloadTSr)
	if err != nil {
		return nil, err
	}
	initiatorSelectors, err := parseTrafficSelectors(finalTSi)
	if err != nil {
		return nil, err
	}
	responderSelectors, err := parseTrafficSelectors(finalTSr)
	if err != nil {
		return nil, err
	}
	cpPayload, err := onePayload(finalPayloads, payloadCP)
	if err != nil {
		return nil, err
	}
	network, err := parseConfiguration(cpPayload)
	if err != nil {
		return nil, err
	}
	if network.LocalIPv4 == nil && network.LocalIPv6 == nil {
		return nil, errors.New("ike: responder did not assign an inner IP address")
	}
	network.PCSCF = pcscfForAssignedFamilies(
		network.PCSCF,
		network.LocalIPv4 != nil,
		network.LocalIPv6 != nil,
	)
	if len(network.PCSCF) == 0 {
		return nil, errors.New("ike: responder did not provide a P-CSCF matching an assigned address family")
	}
	outboundEncryption, outboundIntegrity, inboundEncryption, inboundIntegrity, err := deriveChildSAKeys(
		ikeSuite, childSuite, keys.SKd, initiatorNonce, responderNonce,
	)
	if err != nil {
		return nil, err
	}
	encryptionName, integrityName := espSuiteNames(childSuite)
	name := tunnelName(request.DeviceID)
	relay := newSessionRelay(
		transport,
		ikeSuite,
		keys,
		initiatorSPI,
		responseHeader.ResponderSPI,
		messageID+1,
		natDetected,
		provider.config.KeepaliveInterval,
	)
	installed, err := provider.config.Installer.Install(ctx, ChildSAConfig{
		Name:               name,
		OuterLocal:         append(net.IP(nil), transport.LocalAddr().IP...),
		OuterRemote:        append(net.IP(nil), transport.RemoteAddr().IP...),
		InnerLocalIPv4:     append(net.IP(nil), network.LocalIPv4...),
		InnerLocalIPv6:     append(net.IP(nil), network.LocalIPv6...),
		InnerIPv6Prefix:    network.IPv6Prefix,
		PCSCF:              cloneIPs(network.PCSCF),
		DNS:                cloneIPs(network.DNS),
		InboundSPI:         childInboundSPI,
		OutboundSPI:        childOutboundSPI,
		Encryption:         encryptionName,
		Integrity:          integrityName,
		InboundEncKey:      inboundEncryption,
		InboundAuthKey:     inboundIntegrity,
		OutboundEncKey:     outboundEncryption,
		OutboundAuthKey:    outboundIntegrity,
		InitiatorSelectors: initiatorSelectors,
		ResponderSelectors: responderSelectors,
		UDPEncapsulation:   natDetected,
		ProxyMode:          request.Proxy.Mode,
		Relay:              relay,
	})
	if err != nil {
		_ = relay.Close()
		return nil, fmt.Errorf("ike: install CHILD_SA: %w", err)
	}
	if installed == nil {
		_ = relay.Close()
		return nil, errors.New("ike: CHILD_SA installer returned a nil handle")
	}
	dataplaneMode := "unknown"
	if mode, ok := installed.(DataplaneEvidence); ok {
		switch mode.DataplaneMode() {
		case "userspace", "xfrm":
			dataplaneMode = mode.DataplaneMode()
		}
	}
	evidence := vowifi.TunnelEvidence{
		Established:   true,
		Name:          name,
		DataplaneMode: dataplaneMode,
		LocalIPv4:     ipString(network.LocalIPv4),
		LocalIPv6:     ipString(network.LocalIPv6),
		PCSCF:         ipStrings(network.PCSCF),
		ResponderAUTH: responderAUTH,
		IKEEncryption: fmt.Sprintf("aes-cbc-%d", ikeSuite.EncryptionBits),
		IKEIntegrity:  ikeIntegrityName(ikeSuite.IntegrityID),
		IKEDHGroup:    dhName(ikeSuite.DHID),
		ESPEncryption: encryptionName,
		ESPIntegrity:  integrityName,
	}
	session := &Session{
		evidence: evidence,
		network: NetworkEvidence{
			LocalIPv4:     ipString(network.LocalIPv4),
			LocalIPv6:     ipString(network.LocalIPv6),
			DNS:           ipStrings(network.DNS),
			PCSCF:         ipStrings(network.PCSCF),
			DataplaneMode: dataplaneMode,
		},
		child:     installed,
		relay:     relay,
		transport: transport,
	}
	closeTransport = false
	return session, nil
}

func telefonicaGermanySHA1Compatibility(identity vowifi.SIMIdentity) bool {
	if strings.TrimSpace(identity.HomeMCC) != "262" {
		return false
	}
	mnc := strings.TrimLeft(strings.TrimSpace(identity.HomeMNC), "0")
	if mnc == "" {
		mnc = "0"
	}
	switch mnc {
	case "3", "5", "7", "8", "11", "16", "17", "77":
		return true
	default:
		return false
	}
}

func legacyIKEProfile(mcc, mnc string) bool {
	plmn := strings.TrimSpace(mcc) + strings.TrimLeft(strings.TrimSpace(mnc), "0")
	return plmn == "23415" || plmn == "2044"
}

func advertiseEAPOnlyAuthentication(mcc, mnc string) bool {
	// O2 Germany's 262-03 ePDG rejects an explicit RFC 5998 EAP-only notify;
	// other tested PLMNs still require it for the Android-compatible exchange.
	return !o2GermanyIKECompatibility(mcc, mnc)
}

func o2GermanyIKECompatibility(mcc, mnc string) bool {
	plmn := strings.TrimSpace(mcc) + strings.TrimLeft(strings.TrimSpace(mnc), "0")
	return plmn == "2623"
}

func buildInitialEAPAuth(
	idi payload,
	requestedIDr payload,
	childOfferBody []byte,
	tsi payload,
	tsr payload,
	eapOnly bool,
) []payload {
	payloads := []payload{idi, requestedIDr}
	if eapOnly {
		payloads = append(payloads, makeNotify(notifyEAPOnlyAuth, nil))
	}
	payloads = append(payloads,
		makeNotify(notifyMOBIKESupported, nil),
		makeNotify(notifyInitialContact, nil),
	)
	return append(payloads,
		payload{Type: payloadSA, Body: append([]byte(nil), childOfferBody...)},
		tsi,
		tsr,
		configurationRequest(),
	)
}

func buildInitialEAPOnlyAuth(
	idi payload,
	requestedIDr payload,
	childOfferBody []byte,
	tsi payload,
	tsr payload,
) []payload {
	return buildInitialEAPAuth(idi, requestedIDr, childOfferBody, tsi, tsr, true)
}

func ikeOffer(group uint16, allowSHA1 bool) proposal {
	transforms := []transform{
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 256},
	}
	if allowSHA1 {
		transforms = append(transforms,
			transform{Type: transformPRF, ID: prfHMACSHA256},
			transform{Type: transformPRF, ID: prfHMACSHA1},
			transform{Type: transformIntegrity, ID: integrityHMACSHA256_128},
			transform{Type: transformIntegrity, ID: integrityHMACSHA1_96},
		)
	} else {
		transforms = append(transforms,
			transform{Type: transformPRF, ID: prfHMACSHA256},
			transform{Type: transformIntegrity, ID: integrityHMACSHA256_128},
		)
	}
	transforms = append(transforms, transform{Type: transformDH, ID: group})
	return proposal{Number: 1, Protocol: protocolIKE, Transforms: transforms}
}

func espOffer(spi []byte, allowSHA1 bool) proposal {
	transforms := []transform{
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 256},
	}
	if allowSHA1 {
		transforms = append(transforms,
			transform{Type: transformIntegrity, ID: integrityHMACSHA256_128},
			transform{Type: transformIntegrity, ID: integrityHMACSHA1_96},
		)
	} else {
		transforms = append(transforms, transform{Type: transformIntegrity, ID: integrityHMACSHA256_128})
	}
	transforms = append(transforms, transform{Type: transformESN, ID: 0})
	return proposal{Number: 1, Protocol: protocolESP, SPI: append([]byte(nil), spi...), Transforms: transforms}
}

func validateResponse(
	packet []byte,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	exchange uint8,
	messageID uint32,
) (ikeHeader, []byte, error) {
	header, body, err := parseIKEPacket(packet)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	if header.InitiatorSPI != initiatorSPI ||
		(responderSPI != [8]byte{} && header.ResponderSPI != responderSPI) ||
		header.Exchange != exchange ||
		header.MessageID != messageID ||
		header.Flags&flagResponse == 0 ||
		header.Flags&flagInitiator != 0 {
		return ikeHeader{}, nil, fmt.Errorf("%w: response header does not match the request", errUnexpectedPacket)
	}
	return header, body, nil
}

func decryptAndValidate(
	packet []byte,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	exchange uint8,
	messageID uint32,
	suite negotiatedSuite,
	keys ikeKeys,
) (ikeHeader, []payload, error) {
	header, payloads, err := decryptPayloads(packet, suite, keys.SKer, keys.SKar)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	if header.InitiatorSPI != initiatorSPI ||
		header.ResponderSPI != responderSPI ||
		header.Exchange != exchange ||
		header.MessageID != messageID ||
		header.Flags&flagResponse == 0 ||
		header.Flags&flagInitiator != 0 {
		return ikeHeader{}, nil, fmt.Errorf("%w: encrypted response header does not match the request", errUnexpectedPacket)
	}
	return header, payloads, nil
}

func rejectFatalNotifications(payloads []payload) error {
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err != nil {
			return err
		}
		switch kind {
		case notifyNoProposal:
			return errors.New("ike: responder reported NO_PROPOSAL_CHOSEN")
		case notifyInvalidKE:
			if len(data) == 2 {
				return fmt.Errorf("ike: responder requires DH group %d", binary.BigEndian.Uint16(data))
			}
			return errors.New("ike: responder reported INVALID_KE_PAYLOAD")
		}
		if kind < 16384 {
			return fmt.Errorf("ike: responder reported fatal notification %d", kind)
		}
	}
	return nil
}

func detectNAT(
	payloads []payload,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	local *net.UDPAddr,
	remote *net.UDPAddr,
) (bool, error) {
	var sourceValue, destinationValue []byte
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err != nil {
			return false, err
		}
		switch kind {
		case notifyNATSource:
			sourceValue = data
		case notifyNATDestination:
			destinationValue = data
		}
	}
	if len(sourceValue) == 0 && len(destinationValue) == 0 {
		return false, nil
	}
	if len(sourceValue) != sha1Size || len(destinationValue) != sha1Size {
		return false, errors.New("ike: NAT detection notification has an invalid hash length")
	}
	expectedSource, err := natDetectionHash(initiatorSPI, responderSPI, remote.IP, uint16(remote.Port))
	if err != nil {
		return false, err
	}
	expectedDestination, err := natDetectionHash(initiatorSPI, responderSPI, local.IP, uint16(local.Port))
	if err != nil {
		return false, err
	}
	return !equalBytes(sourceValue, expectedSource) || !equalBytes(destinationValue, expectedDestination), nil
}

const sha1Size = 20

func equalBytes(first, second []byte) bool {
	if len(first) != len(second) {
		return false
	}
	var difference byte
	for index := range first {
		difference |= first[index] ^ second[index]
	}
	return difference == 0
}

func fillNonzero(random io.Reader, destination []byte) error {
	for attempt := 0; attempt < 8; attempt++ {
		if _, err := io.ReadFull(random, destination); err != nil {
			return fmt.Errorf("ike: generate SPI: %w", err)
		}
		var aggregate byte
		for _, value := range destination {
			aggregate |= value
		}
		if aggregate != 0 {
			return nil
		}
	}
	return errors.New("ike: random source generated a zero SPI repeatedly")
}

func tunnelName(deviceID string) string {
	// IFNAMSIZ leaves 15 visible bytes. Hash the complete stable device ID so
	// devices with the same long USB/product prefix never collide at TUNSETIFF.
	normalized := strings.ToLower(strings.TrimSpace(deviceID))
	digest := sha256.Sum256([]byte(normalized))
	return "vocat" + hex.EncodeToString(digest[:5])
}

func ipString(ip net.IP) string {
	if ip == nil {
		return ""
	}
	return ip.String()
}

func ipStrings(ips []net.IP) []string {
	result := make([]string, 0, len(ips))
	for _, ip := range ips {
		if value := ipString(ip); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func pcscfForAssignedFamilies(ips []net.IP, hasIPv4 bool, hasIPv6 bool) []net.IP {
	result := make([]net.IP, 0, len(ips))
	seen := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		key := ip.String()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		switch {
		case ip.To4() != nil && hasIPv4:
			result = append(result, append(net.IP(nil), ip...))
			seen[key] = struct{}{}
		case ip.To4() == nil && ip.To16() != nil && hasIPv6:
			result = append(result, append(net.IP(nil), ip...))
			seen[key] = struct{}{}
		}
	}
	return result
}

func cloneIPs(ips []net.IP) []net.IP {
	result := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		result = append(result, append(net.IP(nil), ip...))
	}
	return result
}

func ikeIntegrityName(identifier uint16) string {
	switch identifier {
	case integrityHMACSHA1_96:
		return "hmac-sha1-96"
	case integrityHMACSHA256_128:
		return "hmac-sha2-256-128"
	default:
		return ""
	}
}

func dhName(identifier uint16) string {
	switch identifier {
	case dhMODP1024:
		return "modp1024"
	case dhMODP2048:
		return "modp2048"
	default:
		return ""
	}
}

type NetworkEvidence struct {
	LocalIPv4     string
	LocalIPv6     string
	DNS           []string
	PCSCF         []string
	DataplaneMode string
}

type Session struct {
	mu        sync.Mutex
	evidence  vowifi.TunnelEvidence
	network   NetworkEvidence
	child     ChildSAHandle
	relay     *sessionRelay
	transport datagramTransport
	closed    bool
}

func (session *Session) Evidence() vowifi.TunnelEvidence {
	session.mu.Lock()
	defer session.mu.Unlock()
	evidence := session.evidence
	evidence.PCSCF = append([]string(nil), session.evidence.PCSCF...)
	return evidence
}

func (session *Session) Network() NetworkEvidence {
	session.mu.Lock()
	defer session.mu.Unlock()
	network := session.network
	network.DNS = append([]string(nil), session.network.DNS...)
	network.PCSCF = append([]string(nil), session.network.PCSCF...)
	return network
}

func (session *Session) Failures() <-chan error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if notifier, ok := session.child.(DataplaneFailureNotifier); ok {
		return notifier.Failures()
	}
	return nil
}

func (session *Session) Close(ctx context.Context) error {
	session.mu.Lock()
	if session.closed {
		session.mu.Unlock()
		return nil
	}
	session.closed = true
	child := session.child
	relay := session.relay
	transport := session.transport
	session.evidence.Established = false
	session.child = nil
	session.relay = nil
	session.transport = nil
	session.mu.Unlock()
	var errs []error
	// Stop the relay first so a userspace CHILD_SA cannot remain blocked in an
	// inbound ESP receive while Close waits for its data-plane workers.
	if relay != nil {
		if err := relay.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close session relay: %w", err))
		}
	}
	if child != nil {
		if err := child.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove CHILD_SA: %w", err))
		}
	}
	if transport != nil {
		if err := transport.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close IKE transport: %w", err))
		}
	}
	return errors.Join(errs...)
}

var _ vowifi.TunnelProvider = (*Provider)(nil)
var _ vowifi.TunnelSession = (*Session)(nil)
var _ vowifi.RuntimeFailureNotifier = (*Session)(nil)
