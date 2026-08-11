package ims

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"vocat/internal/vowifi"
)

type fakeIPSecInstaller struct {
	mu      sync.Mutex
	configs []IPSecSAConfig
	handle  *fakeIPSecHandle
}

type fakeIPSecHandle struct {
	mu         sync.Mutex
	closeCount int
}

func (installer *fakeIPSecInstaller) Install(
	_ context.Context,
	config IPSecSAConfig,
) (IPSecSAHandle, error) {
	if err := validateIPSecSAConfig(config); err != nil {
		return nil, err
	}
	installer.mu.Lock()
	defer installer.mu.Unlock()
	installer.configs = append(installer.configs, cloneIPSecSAConfig(config))
	if installer.handle == nil {
		installer.handle = &fakeIPSecHandle{}
	}
	return installer.handle, nil
}

func (installer *fakeIPSecInstaller) installed() []IPSecSAConfig {
	installer.mu.Lock()
	defer installer.mu.Unlock()
	result := make([]IPSecSAConfig, 0, len(installer.configs))
	for _, config := range installer.configs {
		result = append(result, cloneIPSecSAConfig(config))
	}
	return result
}

func (handle *fakeIPSecHandle) Close(context.Context) error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	handle.closeCount++
	return nil
}

func (handle *fakeIPSecHandle) closes() int {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	return handle.closeCount
}

func TestProviderNegotiatesIPSecAndRegistersOverProtectedTCP(t *testing.T) {
	localIP := net.ParseIP("127.0.0.1")
	remoteIP := net.ParseIP("127.0.0.2")
	initial, err := net.ListenTCP("tcp", &net.TCPAddr{IP: remoteIP})
	if err != nil {
		t.Skipf("secondary loopback address is unavailable: %v", err)
	}
	defer initial.Close()
	protected, err := net.ListenTCP("tcp", &net.TCPAddr{IP: remoteIP})
	if err != nil {
		t.Fatalf("ListenTCP(protected) error = %v", err)
	}
	defer protected.Close()
	for _, listener := range []*net.TCPListener{initial, protected} {
		if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("SetDeadline() error = %v", err)
		}
	}

	ueClientPort, err := availableProtectedPort(localIP, 0)
	if err != nil {
		t.Fatalf("availableProtectedPort(client) error = %v", err)
	}
	ueServerPort, err := availableProtectedPort(localIP, ueClientPort)
	if err != nil {
		t.Fatalf("availableProtectedPort(server) error = %v", err)
	}
	pcscfClientPort, err := availableProtectedPort(remoteIP, protected.Addr().(*net.TCPAddr).Port)
	if err != nil {
		t.Fatalf("availableProtectedPort(P-CSCF client) error = %v", err)
	}

	nonceBytes := make([]byte, 32)
	for index := range nonceBytes {
		nonceBytes[index] = byte(index + 1)
	}
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)
	serverEvidence := make(chan protectedRegistrarEvidence, 1)
	serverDone := make(chan error, 1)
	go func() {
		evidence, err := serveProtectedRegistrar(
			initial,
			protected,
			pcscfClientPort,
			nonce,
			ueClientPort,
			ueServerPort,
		)
		if err == nil {
			serverEvidence <- evidence
		}
		serverDone <- err
	}()

	installer := &fakeIPSecInstaller{}
	aka := &recordingAKA{
		result: vowifi.AKAResult{
			RES: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			CK:  []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			IK:  []byte{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31},
		},
	}
	initialAddress := initial.Addr().String()
	provider, err := NewProvider(aka, Config{
		PCSCF:               initialAddress,
		LocalAddress:        localIP.String(),
		Transport:           "tcp",
		TransactionTimeout:  3 * time.Second,
		SecurityMode:        SecurityRequired,
		IPSecInstaller:      installer,
		ProtectedClientPort: ueClientPort,
		ProtectedServerPort: ueServerPort,
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	session, err := provider.Start(context.Background(), vowifi.IMSRequest{
		DeviceID: "modem0",
		Identity: vowifi.SIMIdentity{
			ICCID:   "8901000000000000000",
			IMSI:    "001010123456789",
			HomeMCC: "001",
			HomeMNC: "01",
			SMSC:    "+12025550100",
		},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true,
			LocalIPv4:   localIP.String(),
			PCSCF:       []string{initialAddress},
		}},
	})
	if err != nil {
		t.Fatalf("Provider.Start() error = %v", err)
	}

	evidence := session.Evidence()
	if !evidence.Registered ||
		evidence.RegistrationState != "registered" ||
		evidence.SecurityMode != "ipsec-3gpp" ||
		!evidence.SecurityVerified {
		t.Fatalf("registration evidence = %#v", evidence)
	}
	number, source, ok := vowifi.ExtractAssociatedMSISDN(evidence)
	if !ok || number != "+8613800138000" || source != vowifi.PhoneSourcePAssociatedURI {
		t.Fatalf("ExtractAssociatedMSISDN() = (%q, %q, %t)", number, source, ok)
	}
	if sms, err := session.EnableSMS(context.Background()); err != nil || !sms.Ready {
		t.Fatalf("EnableSMS() = (%#v, %v)", sms, err)
	}

	configs := installer.installed()
	if len(configs) != 1 {
		t.Fatalf("IPsec install count = %d, want 1", len(configs))
	}
	config := configs[0]
	if !config.LocalIP.Equal(localIP) ||
		!config.RemoteIP.Equal(remoteIP) ||
		config.UEClientPort != ueClientPort ||
		config.UEServerPort != ueServerPort ||
		config.PCSCFClientPort != pcscfClientPort ||
		config.PCSCFServerPort != protected.Addr().(*net.TCPAddr).Port {
		t.Fatalf("IPsec config endpoints = %#v", config)
	}
	if got, want := config.EncryptionKey, aka.result.CK; string(got) != string(want) {
		t.Fatalf("encryption key = %v, want CK %v", got, want)
	}
	wantIntegrity := append(append([]byte(nil), aka.result.IK...), 0, 0, 0, 0)
	if string(config.IntegrityKey) != string(wantIntegrity) {
		t.Fatalf("integrity key = %v, want %v", config.IntegrityKey, wantIntegrity)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("protected registrar error = %v", err)
	}
	registrar := <-serverEvidence
	if registrar.securityClient == "" ||
		registrar.securityVerify != registrar.securityServer {
		t.Fatalf("security agreement evidence = %#v", registrar)
	}
	if installer.handle == nil || installer.handle.closes() != 1 {
		t.Fatalf("IPsec handle close count = %v", installer.handle)
	}
}

func TestProviderRequiresSecurityServerBeforeAKA(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
	serverDone := make(chan error, 1)
	go func() {
		packet := make([]byte, 65535)
		count, remote, err := listener.ReadFromUDP(packet)
		if err != nil {
			serverDone <- err
			return
		}
		_, headers, err := parseTestRequest(packet[:count])
		if err != nil {
			serverDone <- err
			return
		}
		if headers["security-client"] == "" ||
			!strings.Contains(headers["authorization"], "integrity-protected=no") {
			serverDone <- fmt.Errorf("initial security headers = %#v", headers)
			return
		}
		serverDone <- func() error {
			_, err := listener.WriteToUDP(testResponse(
				401,
				"Unauthorized",
				headers["call-id"],
				headers["cseq"],
				[]string{
					`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` +
						nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
				},
			), remote)
			return err
		}()
	}()

	aka := &recordingAKA{}
	address := listener.LocalAddr().String()
	provider, err := NewProvider(aka, Config{
		PCSCF:              address,
		LocalAddress:       "127.0.0.1",
		Transport:          "udp",
		TransactionTimeout: 2 * time.Second,
		SecurityMode:       SecurityRequired,
		IPSecInstaller:     &fakeIPSecInstaller{},
	})
	if err != nil {
		t.Fatalf("NewProvider() error = %v", err)
	}
	_, err = provider.Start(context.Background(), vowifi.IMSRequest{
		Identity: vowifi.SIMIdentity{
			IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
		},
		Tunnel: evidenceTunnel{evidence: vowifi.TunnelEvidence{
			Established: true,
			LocalIPv4:   "127.0.0.1",
			PCSCF:       []string{address},
		}},
	})
	if !errors.Is(err, ErrIPSecAgreementRequired) {
		t.Fatalf("Provider.Start() error = %v, want ErrIPSecAgreementRequired", err)
	}
	if len(aka.challenges) != 0 {
		t.Fatalf("AKA challenge count = %d, want 0 before a valid security offer", len(aka.challenges))
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("registrar error = %v", err)
	}
}

func TestProviderRejectsIMSAddressOverridesOutsideTunnelEvidence(t *testing.T) {
	identity := vowifi.SIMIdentity{
		IMSI: "001010123456789", HomeMCC: "001", HomeMNC: "01",
	}
	for _, test := range []struct {
		name       string
		config     Config
		tunnel     vowifi.TunnelEvidence
		errorMatch string
	}{
		{
			name: "P-CSCF",
			config: Config{
				PCSCF:        "127.0.0.1:25000",
				LocalAddress: "127.0.0.3",
				Transport:    "tcp",
				SecurityMode: SecurityDisabled,
			},
			tunnel: vowifi.TunnelEvidence{
				Established: true,
				LocalIPv4:   "127.0.0.3",
				PCSCF:       []string{"127.0.0.2:25000"},
			},
			errorMatch: "P-CSCF",
		},
		{
			name: "local address",
			config: Config{
				PCSCF:        "127.0.0.2:25000",
				LocalAddress: "127.0.0.3",
				Transport:    "tcp",
				SecurityMode: SecurityDisabled,
			},
			tunnel: vowifi.TunnelEvidence{
				Established: true,
				LocalIPv4:   "127.0.0.4",
				PCSCF:       []string{"127.0.0.2:25000"},
			},
			errorMatch: "local address",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, err := NewProvider(&recordingAKA{}, test.config)
			if err != nil {
				t.Fatalf("NewProvider() error = %v", err)
			}
			_, err = provider.Start(context.Background(), vowifi.IMSRequest{
				Identity: identity,
				Tunnel:   evidenceTunnel{evidence: test.tunnel},
			})
			if err == nil || !strings.Contains(err.Error(), test.errorMatch) {
				t.Fatalf("Provider.Start() error = %v, want %q", err, test.errorMatch)
			}
		})
	}
}

type protectedRegistrarEvidence struct {
	securityClient string
	securityServer string
	securityVerify string
}

func serveProtectedRegistrar(
	initialListener *net.TCPListener,
	protectedListener *net.TCPListener,
	pcscfClientPort int,
	nonce string,
	wantUEClientPort int,
	wantUEServerPort int,
) (protectedRegistrarEvidence, error) {
	var result protectedRegistrarEvidence
	initialConnection, err := initialListener.AcceptTCP()
	if err != nil {
		return result, err
	}
	_ = initialConnection.SetDeadline(time.Now().Add(5 * time.Second))
	initialReader := bufio.NewReader(initialConnection)
	packet, err := readTestTCPRequest(initialReader)
	if err != nil {
		_ = initialConnection.Close()
		return result, err
	}
	startLine, headers, err := parseTestRequest(packet)
	if err != nil {
		_ = initialConnection.Close()
		return result, err
	}
	if !strings.HasPrefix(startLine, "REGISTER ") {
		_ = initialConnection.Close()
		return result, fmt.Errorf("initial method = %q, want REGISTER", startLine)
	}
	if headers["require"] != "sec-agree" || headers["proxy-require"] != "sec-agree" {
		_ = initialConnection.Close()
		return result, fmt.Errorf("initial sec-agree headers = %#v", headers)
	}
	result.securityClient = headers["security-client"]
	securityClientItems := splitHeaderValues([]string{result.securityClient})
	if len(securityClientItems) == 0 {
		_ = initialConnection.Close()
		return result, errors.New("initial Security-Client is empty")
	}
	proposal, err := parseSecurityMechanism(securityClientItems[0])
	if err != nil {
		_ = initialConnection.Close()
		return result, fmt.Errorf("parse initial Security-Client: %w", err)
	}
	if proposal.portClient != wantUEClientPort || proposal.portServer != wantUEServerPort {
		_ = initialConnection.Close()
		return result, fmt.Errorf("UE protected ports = (%d, %d)", proposal.portClient, proposal.portServer)
	}
	if !strings.Contains(headers["contact"], net.JoinHostPort("127.0.0.1", strconv.Itoa(wantUEServerPort))) {
		_ = initialConnection.Close()
		return result, fmt.Errorf("initial Contact = %q", headers["contact"])
	}
	authDirectives, err := testDigestDirectives(headers["authorization"])
	if err != nil {
		_ = initialConnection.Close()
		return result, err
	}
	if authDirectives["nonce"] != "" ||
		authDirectives["response"] != "" ||
		authDirectives["integrity-protected"] != "no" {
		_ = initialConnection.Close()
		return result, fmt.Errorf("initial Authorization = %#v", authDirectives)
	}

	pcscfClientSPI, pcscfServerSPI := nonCollidingServerSPIs(
		proposal.spiClient,
		proposal.spiServer,
	)
	result.securityServer = fmt.Sprintf(
		"ipsec-3gpp;q=0.100;alg=hmac-sha-1-96;prot=esp;mod=trans;"+
			"ealg=aes-cbc;spi-c=%d;spi-s=%d;port-c=%d;port-s=%d",
		pcscfClientSPI,
		pcscfServerSPI,
		pcscfClientPort,
		protectedListener.Addr().(*net.TCPAddr).Port,
	)
	callID := headers["call-id"]
	if _, err := initialConnection.Write(testResponse(
		401,
		"Unauthorized",
		callID,
		headers["cseq"],
		[]string{
			`WWW-Authenticate: Digest realm="ims.mnc001.mcc001.3gppnetwork.org", nonce="` +
				nonce + `", algorithm=AKAv1-MD5, qop="auth"`,
			"Security-Server: " + result.securityServer,
		},
	)); err != nil {
		_ = initialConnection.Close()
		return result, err
	}
	_ = initialConnection.Close()

	protectedConnection, err := protectedListener.AcceptTCP()
	if err != nil {
		return result, err
	}
	defer protectedConnection.Close()
	if err := protectedConnection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return result, err
	}
	if got := protectedConnection.RemoteAddr().(*net.TCPAddr).Port; got != wantUEClientPort {
		return result, fmt.Errorf("protected TCP source port = %d, want %d", got, wantUEClientPort)
	}
	protectedReader := bufio.NewReader(protectedConnection)
	packet, err = readTestTCPRequest(protectedReader)
	if err != nil {
		return result, err
	}
	startLine, headers, err = parseTestRequest(packet)
	if err != nil {
		return result, err
	}
	if !strings.HasPrefix(startLine, "REGISTER ") {
		return result, fmt.Errorf("protected method = %q, want REGISTER", startLine)
	}
	if headers["security-client"] != result.securityClient {
		return result, fmt.Errorf(
			"protected Security-Client = %q, want %q",
			headers["security-client"],
			result.securityClient,
		)
	}
	result.securityVerify = headers["security-verify"]
	if result.securityVerify != result.securityServer {
		return result, fmt.Errorf(
			"Security-Verify = %q, want %q",
			result.securityVerify,
			result.securityServer,
		)
	}
	if err := verifyTestAuthorization(headers["authorization"], nonce); err != nil {
		return result, err
	}
	protectedAuth, err := testDigestDirectives(headers["authorization"])
	if err != nil {
		return result, err
	}
	if protectedAuth["integrity-protected"] != "yes" {
		return result, fmt.Errorf("protected Authorization = %#v", protectedAuth)
	}
	if !strings.Contains(headers["contact"], net.JoinHostPort("127.0.0.1", strconv.Itoa(wantUEServerPort))) {
		return result, fmt.Errorf("protected Contact = %q", headers["contact"])
	}
	if strings.Contains(strings.ToUpper(startLine), "MESSAGE") {
		return result, errors.New("registration transaction sent MESSAGE")
	}
	if !strings.Contains(strings.ToUpper(headers["allow"]), "MESSAGE") {
		return result, errors.New("registration transaction omitted MESSAGE capability")
	}

	if _, err := protectedConnection.Write(testResponse(
		200,
		"OK",
		callID,
		headers["cseq"],
		[]string{
			"P-Associated-URI: <sip:001010123456789@ims.mnc001.mcc001.3gppnetwork.org>, <tel:+8613800138000>",
			"Contact: " + headers["contact"] + ";expires=600",
			"Service-Route: <sip:route.ims.example;lr>",
		},
	)); err != nil {
		return result, err
	}

	packet, err = readTestTCPRequest(protectedReader)
	if err != nil {
		return result, err
	}
	startLine, headers, err = parseTestRequest(packet)
	if err != nil {
		return result, err
	}
	if !strings.HasPrefix(startLine, "REGISTER ") || headers["expires"] != "0" {
		return result, fmt.Errorf("deregistration request = %q, headers %#v", startLine, headers)
	}
	if headers["security-verify"] != result.securityServer {
		return result, fmt.Errorf("deregistration Security-Verify = %q", headers["security-verify"])
	}
	if _, err := protectedConnection.Write(
		testResponse(200, "OK", callID, headers["cseq"], nil),
	); err != nil {
		return result, err
	}
	return result, nil
}

func readTestTCPRequest(reader *bufio.Reader) ([]byte, error) {
	var request strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		request.WriteString(line)
		if line == "\r\n" || line == "\n" {
			return []byte(request.String()), nil
		}
		if request.Len() > 64*1024 {
			return nil, errors.New("SIP request headers are too large")
		}
	}
}

func testDigestDirectives(value string) (map[string]string, error) {
	scheme, parameters, found := strings.Cut(value, " ")
	if !found || !strings.EqualFold(scheme, "Digest") {
		return nil, errors.New("Authorization is not Digest")
	}
	return parseAuthDirectives(parameters)
}

func nonCollidingServerSPIs(ueClient uint32, ueServer uint32) (uint32, uint32) {
	client := uint32(0x70000001)
	for client == ueClient || client == ueServer || client == 0 {
		client++
	}
	server := client + 1
	for server == ueClient || server == ueServer || server == client || server == 0 {
		server++
	}
	return client, server
}
