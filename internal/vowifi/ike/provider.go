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
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"vocat/internal/vowifi"
)

type Config struct {
	Random               io.Reader
	Resolver             *net.Resolver
	Dialer               *net.Dialer
	RootCAs              *x509.CertPool
	ResponderPublicKey   crypto.PublicKey
	ServerName           string
	Timeout              time.Duration
	KeepaliveInterval    time.Duration
	Installer            ChildSAInstaller
	IdentityType         uint8
	APN                  string
	AutoProposalFallback bool
	Logger               *slog.Logger
}

type Provider struct {
	config           Config
	transportFactory func(context.Context, transportConfig, vowifi.ProxyRoute, string) (datagramTransport, error)
}

func NewProvider(config Config) (*Provider, error) {
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Resolver == nil {
		config.Resolver = net.DefaultResolver
	}
	if config.Dialer == nil {
		config.Dialer = &net.Dialer{}
	}
	if config.Timeout < 0 {
		return nil, errors.New("ike: timeout must not be negative")
	}
	if config.Timeout == 0 {
		config.Timeout = 12 * time.Second
	}
	if config.KeepaliveInterval < 0 {
		return nil, errors.New("ike: keepalive interval must not be negative")
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
		return nil, errors.New("ike: APN is invalid")
	}
	if config.Installer == nil {
		config.Installer = defaultChildSAInstaller()
	}
	return &Provider{config: config, transportFactory: newDatagramTransport}, nil
}

func (provider *Provider) Start(ctx context.Context, request vowifi.TunnelRequest) (vowifi.TunnelSession, error) {
	if provider == nil {
		return nil, errors.New("ike: nil provider")
	}
	session, err := provider.start(ctx, request, false)
	if err == nil || !provider.config.AutoProposalFallback {
		return session, err
	}
	profile := vowifi.ResolveCarrierProfile(request.Identity)
	if profile.ID != vowifi.CarrierProfileStandard || !retryableLegacyProposal(err) {
		return nil, err
	}
	provider.config.Logger.Warn("IKE ePDG rejected modern proposal; trying bounded legacy fallback",
		"carrier_profile", profile.ID, "from_proposal", vowifi.IKEProposalModern,
		"to_proposal", vowifi.IKEProposalLegacy, "error", err)
	session, fallbackErr := provider.start(ctx, request, true)
	if fallbackErr != nil {
		return nil, errors.Join(err, fmt.Errorf("ike: legacy proposal fallback failed: %w", fallbackErr))
	}
	provider.config.Logger.Info("IKE automatic legacy proposal fallback succeeded",
		"carrier_profile", profile.ID, "proposal", vowifi.IKEProposalLegacy)
	return session, nil
}

func (provider *Provider) start(ctx context.Context, request vowifi.TunnelRequest, forceLegacy bool) (vowifi.TunnelSession, error) {
	if provider == nil {
		return nil, errors.New("ike: nil provider")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.AKA == nil {
		return nil, errors.New("ike: AKA provider is required")
	}
	epdg := strings.TrimSpace(request.EPDG)
	if epdg == "" || strings.ContainsAny(epdg, " \t\r\n/:") {
		return nil, errors.New("ike: ePDG must be a hostname")
	}
	aka, err := newAKAClient(request.Identity, request.AKA)
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
	carrierProfile := vowifi.ResolveCarrierProfile(request.Identity)
	legacyFirst := carrierProfile.IKEProposal == vowifi.IKEProposalLegacy
	if forceLegacy {
		legacyFirst = true
	}
	advertiseEAPOnly := carrierProfile.AdvertiseEAPOnly
	if legacyFirst {
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
	ikeProposalBody, err := marshalProposals([]proposal{ikeOffer(group, legacyFirst)})
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
		makeNotify(notifyFragmentationSupported, nil),
	}
	var (
		initRequest          []byte
		initResponse         []byte
		responseHeader       ikeHeader
		initResponsePayloads []payload
		cookie               []byte
	)
	for attempt := 0; attempt < maxIKEInitCookieChallenges; attempt++ {
		requestPayloads := append([]payload(nil), initPayloads...)
		if len(cookie) > 0 {
			requestPayloads = append([]payload{makeNotify(notifyCookie, cookie)}, requestPayloads...)
		}
		first, initBody, err := marshalPayloadChain(requestPayloads)
		if err != nil {
			return nil, err
		}
		initRequest = ikeHeader{
			InitiatorSPI: initiatorSPI,
			NextPayload:  first,
			Exchange:     exchangeIKEInit,
			Flags:        flagInitiator,
			MessageID:    0,
		}.marshal(initBody)
		initResponse, err = transport.RoundTrip(ctx, initRequest)
		if err != nil {
			return nil, err
		}
		var responseBody []byte
		responseHeader, responseBody, err = validateResponse(initResponse, initiatorSPI, [8]byte{}, exchangeIKEInit, 0)
		if err != nil {
			return nil, err
		}
		initResponsePayloads, err = parsePayloadChain(responseHeader.NextPayload, responseBody)
		if err != nil {
			return nil, err
		}
		if err := rejectFatalNotifications(initResponsePayloads); err != nil {
			return nil, err
		}
		challenge, hasCookie, err := ikeInitCookie(initResponsePayloads)
		if err != nil {
			return nil, err
		}
		if hasCookie {
			if attempt+1 == maxIKEInitCookieChallenges {
				return nil, errors.New("ike: ePDG requested too many COOKIE challenges")
			}
			cookie = challenge
			continue
		}
		if responseHeader.ResponderSPI == [8]byte{} {
			return nil, errors.New("ike: responder returned a zero SPI")
		}
		break
	}
	peerSupportsFragmentation := hasNotifyType(initResponsePayloads, notifyFragmentationSupported)
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
	cleanupPendingIKE := true
	cleanupMessageID := uint32(2)
	defer func() {
		if cleanupPendingIKE {
			cleanupContext, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = sendIKESADelete(
				cleanupContext,
				transport,
				ikeSuite,
				keys,
				initiatorSPI,
				responseHeader.ResponderSPI,
				cleanupMessageID,
			)
		}
	}()

	var childInboundSPIBytes [4]byte
	if err := fillNonzero(provider.config.Random, childInboundSPIBytes[:]); err != nil {
		return nil, err
	}
	childInboundSPI := binary.BigEndian.Uint32(childInboundSPIBytes[:])
	childOfferBody, err := marshalProposals([]proposal{espOffer(childInboundSPIBytes[:], legacyFirst)})
	if err != nil {
		return nil, err
	}
	idi := payload{Type: payloadIDi, Body: append([]byte{provider.config.IdentityType, 0, 0, 0}, aka.identity...)}
	requestedIDr := payload{Type: payloadIDr, Body: append([]byte{2, 0, 0, 0}, []byte(provider.config.APN)...)}
	tsi := dualStackTrafficSelectors(payloadTSi)
	tsr := dualStackTrafficSelectors(payloadTSr)
	firstAuthPayloads := buildInitialEAPAuth(idi, requestedIDr, childOfferBody, tsi, tsr, advertiseEAPOnly)
	authHeader := ikeHeader{
		InitiatorSPI: initiatorSPI,
		ResponderSPI: responseHeader.ResponderSPI,
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    1,
	}
	_, authResponsePayloads, err := sendAndReceiveIKEPayloads(
		ctx,
		transport,
		authHeader,
		firstAuthPayloads,
		ikeSuite,
		keys,
		peerSupportsFragmentation,
		provider.config.Random,
	)
	if err != nil {
		return nil, err
	}
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
		"", // Android Iwlan enables IKE_OPTION_ACCEPT_ANY_REMOTE_ID.
		serverName,
		provider.config.RootCAs,
		provider.config.ResponderPublicKey,
		true, // Some ePDGs, including O2 Germany, implicitly defer AUTH without accepting the RFC 5998 notify.
	)
	if err != nil {
		return nil, err
	}
	deviceIdentityPending, err := deviceIdentityRequested(authResponsePayloads)
	if err != nil {
		return nil, err
	}
	if deviceIdentityPending {
		provider.config.Logger.Debug("IKE DEVICE_IDENTITY request received",
			"notify_type", notifyDeviceIdentity,
			"stage", "ike_auth",
		)
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
			return nil, errors.New("ike: EAP state machine produced no response")
		}
		messageID++
		cleanupMessageID = messageID + 1
		requestPayloads := []payload{{Type: payloadEAP, Body: action.Response}}
		if deviceIdentityPending && responderAUTH == vowifi.ResponderAUTHVerified {
			deviceIMEI, _ := vowifi.ResolveIMSDeviceIdentity(request.Identity)
			deviceIdentity, identityErr := deviceIdentityNotify(deviceIMEI)
			if identityErr == nil {
				requestPayloads = append(requestPayloads, deviceIdentity)
				identity := strings.TrimSpace(deviceIMEI)
				identityType := "IMEI"
				if len(identity) == 16 {
					identityType = "IMEISV"
				}
				provider.config.Logger.Debug("IKE DEVICE_IDENTITY response sent",
					"notify_type", notifyDeviceIdentity,
					"identity_type", identityType,
					"imei", identity,
					"identity_digits", len(identity),
					"payload_bytes", len(deviceIdentity.Body),
					"stage", "ike_auth",
				)
			} else {
				provider.config.Logger.Warn("IKE DEVICE_IDENTITY response unavailable",
					"notify_type", notifyDeviceIdentity,
					"identity_source", "SIMIdentity.IMEI",
					"stage", "ike_auth",
					"error", identityErr,
				)
			}
		}
		eapHeader := ikeHeader{
			InitiatorSPI: initiatorSPI,
			ResponderSPI: responseHeader.ResponderSPI,
			Exchange:     exchangeIKEAuth,
			Flags:        flagInitiator,
			MessageID:    messageID,
		}
		if requested, notifyErr := deviceIdentityRequested(currentPayloads); notifyErr != nil {
			return nil, notifyErr
		} else if requested {
			if !deviceIdentityPending {
				provider.config.Logger.Debug("IKE DEVICE_IDENTITY request received",
					"notify_type", notifyDeviceIdentity,
					"stage", "ike_auth",
				)
			}
			deviceIdentityPending = true
		}
		_, currentPayloads, err = sendAndReceiveIKEPayloads(
			ctx,
			transport,
			eapHeader,
			requestPayloads,
			ikeSuite,
			keys,
			peerSupportsFragmentation,
			provider.config.Random,
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
	cleanupMessageID = messageID + 1
	finalHeader := ikeHeader{
		InitiatorSPI: initiatorSPI,
		ResponderSPI: responseHeader.ResponderSPI,
		Exchange:     exchangeIKEAuth,
		Flags:        flagInitiator,
		MessageID:    messageID,
	}
	_, finalPayloads, err := sendAndReceiveIKEPayloads(
		ctx,
		transport,
		finalHeader,
		[]payload{initiatorAUTH},
		ikeSuite,
		keys,
		peerSupportsFragmentation,
		provider.config.Random,
	)
	if err != nil {
		return nil, err
	}
	if err := rejectFatalNotifications(finalPayloads); err != nil {
		return nil, err
	}
	finalAUTHs := payloadsOfType(finalPayloads, payloadAuth)
	if len(finalAUTHs) != 1 {
		return nil, fmt.Errorf("%w: final EAP response must contain exactly one MSK AUTH payload", vowifi.ErrResponderAUTHRequired)
	}
	if len(responderID.Body) == 0 {
		return nil, errors.New("ike: EAP exchange has no responder IDr for the AUTH transcript")
	}
	finalIDs := payloadsOfType(finalPayloads, payloadIDr)
	if len(finalIDs) > 1 {
		return nil, errors.New("ike: duplicate final responder IDr payload")
	}
	if len(finalIDs) == 1 {
		if err := validateFQDNIDr(finalIDs[0], "", "final responder"); err != nil {
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
	cleanupPendingIKE = false
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
		_ = relay.CloseWithDelete(ctx)
		return nil, fmt.Errorf("ike: install CHILD_SA: %w", err)
	}
	if installed == nil {
		_ = relay.CloseWithDelete(ctx)
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

const maxIKEInitCookieChallenges = 2

func ikeInitCookie(payloads []payload) ([]byte, bool, error) {
	var cookie []byte
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err != nil {
			return nil, false, err
		}
		if kind != notifyCookie {
			continue
		}
		if len(data) == 0 {
			return nil, false, errors.New("ike: ePDG returned an empty COOKIE")
		}
		if cookie != nil {
			return nil, false, errors.New("ike: ePDG returned multiple COOKIE notifications")
		}
		cookie = append([]byte(nil), data...)
	}
	return cookie, cookie != nil, nil
}

func buildInitialEAPAuth(
	idi payload,
	requestedIDr payload,
	childOfferBody []byte,
	tsi payload,
	tsr payload,
	eapOnly bool,
) []payload {
	// Match Android's IkeSessionStateMachine.buildIkeAuthReq ordering. Some
	// carrier ePDGs inspect this first encrypted exchange before starting EAP.
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

func ikeOffer(group uint16, legacyFirst bool) proposal {
	transforms := []transform{
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 256},
		{Type: transformPRF, ID: prfHMACSHA1},
		{Type: transformPRF, ID: prfHMACSHA256},
		{Type: transformIntegrity, ID: integrityHMACSHA1_96},
		{Type: transformIntegrity, ID: integrityHMACSHA256_128},
		{Type: transformDH, ID: group},
	}
	if !legacyFirst {
		transforms[2], transforms[3] = transforms[3], transforms[2]
		transforms[4], transforms[5] = transforms[5], transforms[4]
	}
	return proposal{Number: 1, Protocol: protocolIKE, Transforms: transforms}
}

func espOffer(spi []byte, legacyFirst bool) proposal {
	transforms := []transform{
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 128},
		{Type: transformEncryption, ID: encryptionAESCBC, KeyLength: 256},
		{Type: transformIntegrity, ID: integrityHMACSHA1_96},
		{Type: transformIntegrity, ID: integrityHMACSHA256_128},
		{Type: transformESN, ID: 0},
	}
	if !legacyFirst {
		transforms[2], transforms[3] = transforms[3], transforms[2]
	}
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
	header, payloads, err := decryptPayloadsAny(packet, nil, suite, keys.SKer, keys.SKar)
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

func decryptAndValidateFragments(
	packets [][]byte,
	initiatorSPI [8]byte,
	responderSPI [8]byte,
	exchange uint8,
	messageID uint32,
	suite negotiatedSuite,
	keys ikeKeys,
) (ikeHeader, []payload, error) {
	if len(packets) == 0 {
		return ikeHeader{}, nil, errors.New("ike: empty exchange response")
	}
	if len(packets) == 1 {
		return decryptAndValidate(packets[0], initiatorSPI, responderSPI, exchange, messageID, suite, keys)
	}
	header, payloads, err := decryptPayloadsAny(nil, packets, suite, keys.SKer, keys.SKar)
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

func sendAndReceiveIKEPayloads(
	ctx context.Context,
	transport datagramTransport,
	header ikeHeader,
	payloads []payload,
	suite negotiatedSuite,
	keys ikeKeys,
	peerSupportsFragmentation bool,
	random io.Reader,
) (ikeHeader, []payload, error) {
	var outboundPackets [][]byte
	var err error
	if peerSupportsFragmentation {
		outboundPackets, err = encryptPayloadsFragmented(header, payloads, suite, keys.SKei, keys.SKai, defaultIKEFragmentSize, random)
	} else {
		pkt, encryptErr := encryptPayloads(header, payloads, suite, keys.SKei, keys.SKai, random)
		if encryptErr != nil {
			return ikeHeader{}, nil, encryptErr
		}
		outboundPackets = [][]byte{pkt}
	}
	if err != nil {
		return ikeHeader{}, nil, err
	}
	inboundPackets, err := transport.RoundTripExchange(ctx, outboundPackets)
	if err != nil {
		return ikeHeader{}, nil, err
	}
	return decryptAndValidateFragments(
		inboundPackets,
		header.InitiatorSPI,
		header.ResponderSPI,
		header.Exchange,
		header.MessageID,
		suite,
		keys,
	)
}

func hasNotifyType(payloads []payload, notifyType uint16) bool {
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, _, err := parseNotify(item)
		if err == nil && kind == notifyType {
			return true
		}
	}
	return false
}

var errNoProposalChosen = errors.New("ike: responder reported NO_PROPOSAL_CHOSEN")

type invalidKEPayloadError struct {
	group uint16
}

func (err *invalidKEPayloadError) Error() string {
	if err.group != 0 {
		return fmt.Sprintf("ike: responder requires DH group %d", err.group)
	}
	return "ike: responder reported INVALID_KE_PAYLOAD"
}

func retryableLegacyProposal(err error) bool {
	if errors.Is(err, errNoProposalChosen) {
		return true
	}
	var invalidKE *invalidKEPayloadError
	return errors.As(err, &invalidKE) && (invalidKE.group == 0 || invalidKE.group == dhMODP1024)
}

func rejectFatalNotifications(payloads []payload) error {
	for _, item := range payloadsOfType(payloads, payloadNotify) {
		kind, data, err := parseNotify(item)
		if err != nil {
			return err
		}
		switch kind {
		case notifyNoProposal:
			return errNoProposalChosen
		case notifyInvalidKE:
			if len(data) == 2 {
				return &invalidKEPayloadError{group: binary.BigEndian.Uint16(data)}
			}
			return &invalidKEPayloadError{}
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
	if session.relay != nil {
		return session.relay.Failures()
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
	if child != nil {
		if err := child.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("remove CHILD_SA: %w", err))
		}
	}
	if relay != nil {
		if err := relay.CloseWithDelete(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close session relay: %w", err))
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
