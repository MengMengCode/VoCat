package ims

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"vocat/internal/vowifi"
)

type SecurityMode string

const (
	// SecurityRequired is the production default. A 401 response without a
	// supported ipsec-3gpp Security-Server offer fails closed.
	SecurityRequired SecurityMode = "required"
	// SecurityOptional advertises ipsec-3gpp but permits a carrier that
	// explicitly omits Security-Server to continue on the tunnel in plain SIP.
	SecurityOptional SecurityMode = "optional"
	// SecurityDisabled is intended for controlled interoperability testing.
	SecurityDisabled SecurityMode = "disabled"
)

var (
	ErrIPSecAgreementRequired  = errors.New("ims: a supported ipsec-3gpp security agreement is required")
	ErrIPSecInstall            = errors.New("ims: install ipsec-3gpp security associations")
	errIncompleteSecurityOffer = errors.New("ims: incomplete ipsec-3gpp security offer")
)

// IPSecSAConfig is the complete, evidence-derived 3GPP transport-mode SA set.
// The two UE SPIs identify inbound SAs; the two P-CSCF SPIs identify outbound
// SAs. EncryptionKey and IntegrityKey must be discarded after Install returns.
type IPSecSAConfig struct {
	LocalIP  net.IP
	RemoteIP net.IP
	// IntegrityAlgorithm is the negotiated 3GPP Security-Server alg value.
	// Supported values are hmac-md5-96 and hmac-sha-1-96.
	IntegrityAlgorithm string
	// EncryptionAlgorithm is the negotiated 3GPP Security-Server ealg value.
	// Supported values are null, des-ede3-cbc, and aes-cbc.
	EncryptionAlgorithm string

	UEClientSPI    uint32
	UEServerSPI    uint32
	PCSCFClientSPI uint32
	PCSCFServerSPI uint32

	UEClientPort    int
	UEServerPort    int
	PCSCFClientPort int
	PCSCFServerPort int

	EncryptionKey []byte
	IntegrityKey  []byte
}

type IPSecSAHandle interface {
	Close(context.Context) error
}

type IPSecSAInstaller interface {
	Install(context.Context, IPSecSAConfig) (IPSecSAHandle, error)
}

type securityProposal struct {
	spiClient                uint32
	spiServer                uint32
	portClient               int
	portServer               int
	encryption               string
	fallbackEncryption       string
	integrityAlgorithms      []string
	encryptionAlgorithmsList []string
}

func newSecurityProposal(localIP net.IP, configuredClientPort int, configuredServerPort int) (securityProposal, error) {
	spiClient, err := randomSPI(0)
	if err != nil {
		return securityProposal{}, err
	}
	spiServer, err := randomSPI(spiClient)
	if err != nil {
		return securityProposal{}, err
	}
	portClient := configuredClientPort
	if portClient == 0 {
		portClient, err = availableProtectedPort(localIP, 0)
		if err != nil {
			return securityProposal{}, err
		}
	}
	portServer := configuredServerPort
	if portServer == 0 {
		portServer, err = availableProtectedPort(localIP, portClient)
		if err != nil {
			return securityProposal{}, err
		}
	}
	if !validProtectedPort(portClient) || !validProtectedPort(portServer) || portClient == portServer {
		return securityProposal{}, errors.New("ims: protected UE ports must be distinct non-standard SIP ports")
	}
	return securityProposal{
		spiClient:                spiClient,
		spiServer:                spiServer,
		portClient:               portClient,
		portServer:               portServer,
		integrityAlgorithms:      []string{"hmac-md5-96", "hmac-sha-1-96"},
		encryptionAlgorithmsList: []string{"null", "des-ede3-cbc", "aes-cbc"},
	}, nil
}

func (proposal securityProposal) headerValue() string {
	// TS 33.203 defines spi-c and spi-s as exactly 10 decimal digits. Keep the
	// complete mechanism explicit even where ESP/null defaults would permit a
	// shorter form; this is the interoperable handset/IMS profile.
	values := make([]string, 0, len(proposal.integrities())*len(proposal.encryptionAlgorithms()))
	index := 0
	for _, integrity := range proposal.integrities() {
		for _, encryption := range proposal.encryptionAlgorithms() {
			preference := fmt.Sprintf("0.%03d", 999-index)
			if index == 0 {
				preference = "1.000"
			}
			values = append(values, fmt.Sprintf(
				"ipsec-3gpp;q=%s;alg=%s;prot=esp;mod=trans;ealg=%s;spi-c=%010d;spi-s=%010d;port-c=%d;port-s=%d",
				preference,
				integrity,
				encryption,
				proposal.spiClient,
				proposal.spiServer,
				proposal.portClient,
				proposal.portServer,
			))
			index++
		}
	}
	return strings.Join(values, ", ")
}

func (session *Session) securityClientValue() string {
	if vowifi.IsATT310280(session.request.Identity) {
		return fmt.Sprintf(
			"ipsec-3gpp; alg=hmac-sha-1-96; ealg=aes-cbc; prot=esp; mod=trans; spi-c=%d; spi-s=%d; port-c=%d; port-s=%d",
			session.securityProposal.spiClient,
			session.securityProposal.spiServer,
			session.securityProposal.portClient,
			session.securityProposal.portServer,
		)
	}
	return session.securityProposal.headerValue()
}

func (proposal securityProposal) encryptionAlgorithm() string {
	if strings.EqualFold(strings.TrimSpace(proposal.encryption), "null") {
		return "null"
	}
	return "aes-cbc"
}

func (proposal securityProposal) encryptionAlgorithms() []string {
	if len(proposal.encryptionAlgorithmsList) > 0 {
		return append([]string(nil), proposal.encryptionAlgorithmsList...)
	}
	algorithms := []string{proposal.encryptionAlgorithm()}
	fallback := strings.ToLower(strings.TrimSpace(proposal.fallbackEncryption))
	if (fallback == "aes-cbc" || fallback == "null") && fallback != algorithms[0] {
		algorithms = append(algorithms, fallback)
	}
	return algorithms
}

func (proposal securityProposal) integrities() []string {
	if len(proposal.integrityAlgorithms) > 0 {
		return append([]string(nil), proposal.integrityAlgorithms...)
	}
	return []string{"hmac-sha-1-96"}
}

func (proposal securityProposal) supportsIntegrity(integrity string) bool {
	for _, offered := range proposal.integrities() {
		if strings.EqualFold(integrity, offered) {
			return true
		}
	}
	return false
}

func (proposal securityProposal) supportsEncryption(encryption string) bool {
	for _, offered := range proposal.encryptionAlgorithms() {
		if strings.EqualFold(encryption, offered) {
			return true
		}
	}
	return false
}

func randomSPI(exclude uint32) (uint32, error) {
	for attempts := 0; attempts < 16; attempts++ {
		var value [4]byte
		if _, err := rand.Read(value[:]); err != nil {
			return 0, fmt.Errorf("ims: create protected SPI: %w", err)
		}
		spi := binary.BigEndian.Uint32(value[:])
		if spi >= 256 && spi != exclude {
			return spi, nil
		}
	}
	return 0, errors.New("ims: could not allocate a protected SPI")
}

func availableProtectedPort(localIP net.IP, exclude int) (int, error) {
	for attempts := 0; attempts < 32; attempts++ {
		var value [2]byte
		if _, err := rand.Read(value[:]); err != nil {
			return 0, fmt.Errorf("ims: create protected port: %w", err)
		}
		port := 20000 + int(binary.BigEndian.Uint16(value[:]))%44000
		if port == exclude || !validProtectedPort(port) {
			continue
		}
		address := &net.TCPAddr{IP: append(net.IP(nil), localIP...), Port: port}
		listener, err := net.ListenTCP("tcp", address)
		if err != nil {
			continue
		}
		_ = listener.Close()
		packet, err := net.ListenUDP("udp", &net.UDPAddr{IP: append(net.IP(nil), localIP...), Port: port})
		if err != nil {
			continue
		}
		_ = packet.Close()
		return port, nil
	}
	return 0, errors.New("ims: no protected local port is available")
}

func validProtectedPort(port int) bool {
	return port > 1024 && port <= 65535 && port != 5060 && port != 5061
}

type securityMechanism struct {
	raw        string
	name       string
	algorithm  string
	protocol   string
	mode       string
	encryption string
	spiClient  uint32
	spiServer  uint32
	portClient int
	portServer int
	preference int
}

type securityAgreement struct {
	selected    securityMechanism
	verifyValue string
}

func parseSecurityAgreement(values []string, proposal securityProposal) (securityAgreement, error) {
	items := splitHeaderValues(values)
	if len(items) == 0 {
		return securityAgreement{}, ErrIPSecAgreementRequired
	}
	candidates := make([]securityMechanism, 0, len(items))
	for _, item := range items {
		mechanism, err := parseSecurityMechanism(item)
		if err != nil {
			name := strings.ToLower(strings.TrimSpace(strings.SplitN(item, ";", 2)[0]))
			if name == "ipsec-3gpp" {
				if errors.Is(err, errIncompleteSecurityOffer) {
					continue
				}
				return securityAgreement{}, fmt.Errorf(
					"ims: malformed ipsec-3gpp Security-Server: %w",
					err,
				)
			}
			continue
		}
		if !strings.EqualFold(mechanism.name, "ipsec-3gpp") ||
			!proposal.supportsIntegrity(mechanism.algorithm) ||
			!strings.EqualFold(mechanism.protocol, "esp") ||
			!strings.EqualFold(mechanism.mode, "trans") ||
			!proposal.supportsEncryption(mechanism.encryption) {
			continue
		}
		if mechanism.spiClient == 0 || mechanism.spiServer == 0 ||
			mechanism.spiClient == mechanism.spiServer ||
			mechanism.spiClient == proposal.spiClient ||
			mechanism.spiClient == proposal.spiServer ||
			mechanism.spiServer == proposal.spiClient ||
			mechanism.spiServer == proposal.spiServer ||
			!validProtectedPort(mechanism.portClient) ||
			!validProtectedPort(mechanism.portServer) ||
			mechanism.portClient == mechanism.portServer {
			continue
		}
		candidates = append(candidates, mechanism)
	}
	if len(candidates) == 0 {
		return securityAgreement{}, ErrIPSecAgreementRequired
	}
	sort.SliceStable(candidates, func(left int, right int) bool {
		return candidates[left].preference > candidates[right].preference
	})
	return securityAgreement{
		selected:    candidates[0],
		verifyValue: strings.Join(items, ", "),
	}, nil
}

func parseSecurityMechanism(value string) (securityMechanism, error) {
	parts := strings.Split(value, ";")
	if len(parts) == 0 {
		return securityMechanism{}, errors.New("ims: empty Security-Server mechanism")
	}
	mechanism := securityMechanism{
		raw:        strings.TrimSpace(value),
		name:       strings.ToLower(strings.TrimSpace(parts[0])),
		protocol:   "esp",
		mode:       "trans",
		encryption: "null",
	}
	parameters := make(map[string]string)
	for _, raw := range parts[1:] {
		key, parameterValue, found := strings.Cut(strings.TrimSpace(raw), "=")
		if !found {
			return securityMechanism{}, errors.New("ims: malformed Security-Server parameter")
		}
		key = strings.ToLower(strings.TrimSpace(key))
		parameterValue = strings.Trim(strings.TrimSpace(parameterValue), `"`)
		if key == "" || parameterValue == "" {
			return securityMechanism{}, errors.New("ims: empty Security-Server parameter")
		}
		if _, duplicate := parameters[key]; duplicate {
			return securityMechanism{}, errors.New("ims: duplicate Security-Server parameter")
		}
		parameters[key] = parameterValue
	}
	mechanism.algorithm = strings.ToLower(parameters["alg"])
	if value := parameters["prot"]; value != "" {
		mechanism.protocol = strings.ToLower(value)
	}
	if value := parameters["mod"]; value != "" {
		mechanism.mode = strings.ToLower(value)
	}
	if value := parameters["ealg"]; value != "" {
		mechanism.encryption = strings.ToLower(value)
	}
	saParameterKeys := []string{"spi-c", "spi-s", "port-c", "port-s"}
	presentSAParameters := 0
	for _, key := range saParameterKeys {
		if parameters[key] != "" {
			presentSAParameters++
		}
	}
	if presentSAParameters == 0 {
		return securityMechanism{}, errIncompleteSecurityOffer
	}
	if presentSAParameters != len(saParameterKeys) {
		return securityMechanism{}, errors.New("ims: partially specified Security-Server SA parameters")
	}
	var err error
	if mechanism.spiClient, err = decimalUint32(parameters["spi-c"]); err != nil {
		return securityMechanism{}, err
	}
	if mechanism.spiServer, err = decimalUint32(parameters["spi-s"]); err != nil {
		return securityMechanism{}, err
	}
	if mechanism.portClient, err = decimalPort(parameters["port-c"]); err != nil {
		return securityMechanism{}, err
	}
	if mechanism.portServer, err = decimalPort(parameters["port-s"]); err != nil {
		return securityMechanism{}, err
	}
	mechanism.preference, err = preferenceValue(parameters["q"])
	if err != nil {
		return securityMechanism{}, err
	}
	return mechanism, nil
}

func decimalUint32(value string) (uint32, error) {
	if value == "" || len(value) > 10 {
		return 0, errors.New("ims: invalid Security-Server SPI")
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, errors.New("ims: invalid Security-Server SPI")
	}
	return uint32(parsed), nil
}

func decimalPort(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > 65535 {
		return 0, errors.New("ims: invalid Security-Server port")
	}
	return parsed, nil
}

func preferenceValue(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	whole, fraction, found := strings.Cut(value, ".")
	if whole != "0" && whole != "1" {
		return 0, errors.New("ims: invalid Security-Server preference")
	}
	if !found {
		if whole == "1" {
			return 1000, nil
		}
		return 0, nil
	}
	if len(fraction) > 3 {
		return 0, errors.New("ims: invalid Security-Server preference")
	}
	for len(fraction) < 3 {
		fraction += "0"
	}
	numeric, err := strconv.Atoi(fraction)
	if err != nil || (whole == "1" && numeric != 0) {
		return 0, errors.New("ims: invalid Security-Server preference")
	}
	if whole == "1" {
		return 1000, nil
	}
	return numeric, nil
}

func expandIPSecKeys(ck []byte, ik []byte, encryptionAlgorithm, integrityAlgorithm string) (encryption []byte, integrity []byte, err error) {
	if len(ck) != 16 || len(ik) != 16 {
		return nil, nil, errors.New("ims: AKA did not return 16-byte CK and IK")
	}
	switch strings.ToLower(strings.TrimSpace(encryptionAlgorithm)) {
	case "null":
		encryption = nil
	case "aes-cbc", "":
		encryption = append([]byte(nil), ck...)
	case "des-ede3-cbc":
		encryption = append(encryption, ck...)
		encryption = append(encryption, ck[:8]...)
		for index, value := range encryption {
			encryption[index] = withOddDESParity(value)
		}
	default:
		return nil, nil, errors.New("ims: unsupported ipsec-3gpp encryption algorithm")
	}
	switch strings.ToLower(strings.TrimSpace(integrityAlgorithm)) {
	case "hmac-md5-96":
		integrity = append([]byte(nil), ik...)
	case "hmac-sha-1-96", "":
		integrity = make([]byte, 20)
		copy(integrity, ik)
	default:
		return nil, nil, errors.New("ims: unsupported ipsec-3gpp integrity algorithm")
	}
	return encryption, integrity, nil
}

func withOddDESParity(value byte) byte {
	value &^= 1
	ones := 0
	for bits := value; bits != 0; bits >>= 1 {
		ones += int(bits & 1)
	}
	if ones%2 == 0 {
		value |= 1
	}
	return value
}

type xfrmOperation struct {
	description string
	arguments   []string
}

func buildXFRMInstallPlan(config IPSecSAConfig) ([]xfrmOperation, error) {
	if err := validateIPSecSAConfig(config); err != nil {
		return nil, err
	}
	var operations []xfrmOperation
	states := []struct {
		description string
		source      net.IP
		destination net.IP
		spi         uint32
		reqid       uint32
	}{
		{"outbound UE-client to P-CSCF-server state", config.LocalIP, config.RemoteIP, config.PCSCFServerSPI, clientPairReqID(config)},
		{"inbound P-CSCF-server to UE-client state", config.RemoteIP, config.LocalIP, config.UEClientSPI, clientPairReqID(config)},
		{"inbound P-CSCF-client to UE-server state", config.RemoteIP, config.LocalIP, config.UEServerSPI, serverPairReqID(config)},
		{"outbound UE-server to P-CSCF-client state", config.LocalIP, config.RemoteIP, config.PCSCFClientSPI, serverPairReqID(config)},
	}
	for _, state := range states {
		arguments := []string{
			"xfrm", "state", "add",
			"src", state.source.String(),
			"dst", state.destination.String(),
			"proto", "esp",
			"spi", fmt.Sprintf("0x%08x", state.spi),
			"reqid", strconv.FormatUint(uint64(state.reqid), 10),
			"mode", "transport",
			"replay-window", "32",
			"auth-trunc", xfrmIntegrityAlgorithm(config), "0x" + hex.EncodeToString(config.IntegrityKey), "96",
		}
		switch ipsecEncryptionAlgorithm(config) {
		case "aes-cbc":
			arguments = append(arguments, "enc", "cbc(aes)", "0x"+hex.EncodeToString(config.EncryptionKey))
		case "des-ede3-cbc":
			arguments = append(arguments, "enc", "cbc(des3_ede)", "0x"+hex.EncodeToString(config.EncryptionKey))
		default:
			// Linux requires an explicit encryption transform for ESP even when
			// 3GPP negotiates ealg=null. iproute2 must receive a genuinely empty
			// key argument; the textual value "0x" is rejected by XFRM as EINVAL.
			arguments = append(arguments, "enc", "cipher_null", "")
		}
		operations = append(operations, xfrmOperation{
			description: state.description,
			arguments:   arguments,
		})
	}
	for _, flow := range xfrmFlows(config) {
		for _, protocol := range flow.protocols {
			operations = append(operations, xfrmOperation{
				description: flow.description + " " + protocol + " policy",
				arguments:   xfrmPolicyArgs(flow, protocol, false),
			})
		}
	}
	return operations, nil
}

func buildXFRMCleanupPlan(config IPSecSAConfig) []xfrmOperation {
	var operations []xfrmOperation
	flows := xfrmFlows(config)
	for flowIndex := len(flows) - 1; flowIndex >= 0; flowIndex-- {
		flow := flows[flowIndex]
		for protocolIndex := len(flow.protocols) - 1; protocolIndex >= 0; protocolIndex-- {
			protocol := flow.protocols[protocolIndex]
			operations = append(operations, xfrmOperation{
				description: "delete " + flow.description + " " + protocol + " policy",
				arguments:   xfrmPolicyArgs(flow, protocol, true),
			})
		}
	}
	states := []struct {
		source      net.IP
		destination net.IP
		spi         uint32
	}{
		{config.LocalIP, config.RemoteIP, config.PCSCFClientSPI},
		{config.RemoteIP, config.LocalIP, config.UEServerSPI},
		{config.RemoteIP, config.LocalIP, config.UEClientSPI},
		{config.LocalIP, config.RemoteIP, config.PCSCFServerSPI},
	}
	for _, state := range states {
		operations = append(operations, xfrmOperation{
			description: "delete ipsec-3gpp state",
			arguments: []string{
				"xfrm", "state", "delete",
				"src", state.source.String(),
				"dst", state.destination.String(),
				"proto", "esp",
				"spi", fmt.Sprintf("0x%08x", state.spi),
			},
		})
	}
	return operations
}

func xfrmPolicyArgs(flow xfrmFlow, protocol string, delete bool) []string {
	args := []string{
		flow.family,
		"xfrm", "policy",
	}
	if delete {
		args = append(args, "delete")
	} else {
		args = append(args, "add")
	}
	args = append(args,
		"src", flow.sourcePrefix,
		"dst", flow.destinationPrefix,
		"proto", protocol,
	)
	if flow.sourcePort > 0 {
		args = append(args, "sport", strconv.Itoa(flow.sourcePort))
	}
	if flow.destinationPort > 0 {
		args = append(args, "dport", strconv.Itoa(flow.destinationPort))
	}
	args = append(args, "dir", flow.direction)
	if delete {
		return args
	}
	return append(args,
		"priority", "100",
		"tmpl",
		"src", flow.templateSource.String(),
		"dst", flow.templateDestination.String(),
		"proto", "esp",
		"spi", fmt.Sprintf("0x%08x", flow.spi),
		"reqid", strconv.FormatUint(uint64(flow.reqid), 10),
		"mode", "transport",
		"level", "required",
	)
}

type xfrmFlow struct {
	description         string
	family              string
	sourcePrefix        string
	destinationPrefix   string
	sourcePort          int
	destinationPort     int
	direction           string
	templateSource      net.IP
	templateDestination net.IP
	spi                 uint32
	reqid               uint32
	protocols           []string
}

func xfrmFlows(config IPSecSAConfig) []xfrmFlow {
	family := "-4"
	prefix := "/32"
	if config.LocalIP.To4() == nil {
		family = "-6"
		prefix = "/128"
	}
	localPrefix := config.LocalIP.String() + prefix
	remotePrefix := config.RemoteIP.String() + prefix
	return []xfrmFlow{
		{
			description: "UE-client to P-CSCF-server", family: family,
			sourcePrefix: localPrefix, destinationPrefix: remotePrefix,
			sourcePort: config.UEClientPort, destinationPort: config.PCSCFServerPort,
			direction: "out", templateSource: config.LocalIP, templateDestination: config.RemoteIP,
			spi: config.PCSCFServerSPI, reqid: clientPairReqID(config),
			protocols: []string{"tcp", "udp"},
		},
		{
			description: "P-CSCF-server to UE-client", family: family,
			sourcePrefix: remotePrefix, destinationPrefix: localPrefix,
			sourcePort: config.PCSCFServerPort, destinationPort: config.UEClientPort,
			direction: "in", templateSource: config.RemoteIP, templateDestination: config.LocalIP,
			spi: config.UEClientSPI, reqid: clientPairReqID(config),
			protocols: []string{"tcp"},
		},
		{
			description: "P-CSCF-client to UE-server", family: family,
			sourcePrefix: remotePrefix, destinationPrefix: localPrefix,
			sourcePort: 0, destinationPort: config.UEServerPort,
			direction: "in", templateSource: config.RemoteIP, templateDestination: config.LocalIP,
			spi: config.UEServerSPI, reqid: serverPairReqID(config),
			protocols: []string{"tcp", "udp"},
		},
		{
			description: "UE-server to P-CSCF-client", family: family,
			sourcePrefix: localPrefix, destinationPrefix: remotePrefix,
			sourcePort: config.UEServerPort, destinationPort: config.PCSCFClientPort,
			direction: "out", templateSource: config.LocalIP, templateDestination: config.RemoteIP,
			spi: config.PCSCFClientSPI, reqid: serverPairReqID(config),
			protocols: []string{"tcp"},
		},
	}
}

func clientPairReqID(config IPSecSAConfig) uint32 {
	reqid := (config.UEClientSPI ^ config.PCSCFServerSPI) & 0x7fffffff
	if reqid == 0 {
		return 1
	}
	return reqid
}

func serverPairReqID(config IPSecSAConfig) uint32 {
	reqid := (config.UEServerSPI ^ config.PCSCFClientSPI) & 0x7fffffff
	if reqid == 0 {
		reqid = 2
	}
	if reqid == clientPairReqID(config) {
		reqid ^= 0x40000000
		if reqid == 0 {
			reqid = 2
		}
	}
	return reqid
}

func validateIPSecSAConfig(config IPSecSAConfig) error {
	local := config.LocalIP
	remote := config.RemoteIP
	if local == nil || remote == nil || local.IsUnspecified() || remote.IsUnspecified() ||
		(local.To4() == nil) != (remote.To4() == nil) {
		return errors.New("ims: ipsec-3gpp endpoints are invalid or use different IP families")
	}
	spis := []uint32{
		config.UEClientSPI, config.UEServerSPI, config.PCSCFClientSPI, config.PCSCFServerSPI,
	}
	seen := make(map[uint32]struct{}, len(spis))
	for _, spi := range spis {
		if spi == 0 {
			return errors.New("ims: ipsec-3gpp SPI is zero")
		}
		if _, duplicate := seen[spi]; duplicate {
			return errors.New("ims: ipsec-3gpp SPIs must be unique")
		}
		seen[spi] = struct{}{}
	}
	ports := []int{
		config.UEClientPort, config.UEServerPort, config.PCSCFClientPort, config.PCSCFServerPort,
	}
	for _, port := range ports {
		if !validProtectedPort(port) {
			return errors.New("ims: ipsec-3gpp protected port is invalid")
		}
	}
	if config.UEClientPort == config.UEServerPort ||
		config.PCSCFClientPort == config.PCSCFServerPort {
		return errors.New("ims: client and server protected ports must differ")
	}
	if ipsecEncryptionAlgorithm(config) != "null" && ipsecEncryptionAlgorithm(config) != "aes-cbc" && ipsecEncryptionAlgorithm(config) != "des-ede3-cbc" {
		return errors.New("ims: unsupported ipsec-3gpp encryption algorithm")
	}
	if ipsecIntegrityAlgorithm(config) != "hmac-md5-96" && ipsecIntegrityAlgorithm(config) != "hmac-sha-1-96" {
		return errors.New("ims: unsupported ipsec-3gpp integrity algorithm")
	}
	wantEncryptionKey := map[string]int{"null": 0, "aes-cbc": 16, "des-ede3-cbc": 24}[ipsecEncryptionAlgorithm(config)]
	wantIntegrityKey := map[string]int{"hmac-md5-96": 16, "hmac-sha-1-96": 20}[ipsecIntegrityAlgorithm(config)]
	if len(config.EncryptionKey) != wantEncryptionKey || len(config.IntegrityKey) != wantIntegrityKey {
		return errors.New("ims: ipsec-3gpp key length is invalid")
	}
	return nil
}

func ipsecIntegrityAlgorithm(config IPSecSAConfig) string {
	algorithm := strings.ToLower(strings.TrimSpace(config.IntegrityAlgorithm))
	if algorithm == "" {
		return "hmac-sha-1-96"
	}
	return algorithm
}

func xfrmIntegrityAlgorithm(config IPSecSAConfig) string {
	if ipsecIntegrityAlgorithm(config) == "hmac-md5-96" {
		return "hmac(md5)"
	}
	return "hmac(sha1)"
}

func ipsecEncryptionAlgorithm(config IPSecSAConfig) string {
	algorithm := strings.ToLower(strings.TrimSpace(config.EncryptionAlgorithm))
	if algorithm == "" {
		return "aes-cbc"
	}
	return algorithm
}

func cloneIPSecSAConfig(config IPSecSAConfig) IPSecSAConfig {
	config.LocalIP = append(net.IP(nil), config.LocalIP...)
	config.RemoteIP = append(net.IP(nil), config.RemoteIP...)
	config.EncryptionKey = append([]byte(nil), config.EncryptionKey...)
	config.IntegrityKey = append([]byte(nil), config.IntegrityKey...)
	return config
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (session *Session) securityOffered() bool {
	return session.provider != nil && session.provider.config.SecurityMode != SecurityDisabled && !session.securityDeclined
}

func (session *Session) securityFromResponse(response *sipResponse) (securityAgreement, bool, error) {
	if !session.securityOffered() {
		return securityAgreement{}, false, nil
	}
	values := response.values("Security-Server")
	if len(splitHeaderValues(values)) == 0 {
		if session.provider.config.SecurityMode == SecurityRequired {
			return securityAgreement{}, false, ErrIPSecAgreementRequired
		}
		session.declineSecurity()
		return securityAgreement{}, false, nil
	}
	agreement, err := parseSecurityAgreement(values, session.securityProposal)
	if err != nil {
		return securityAgreement{}, false, err
	}
	return agreement, true, nil
}

func (session *Session) declineSecurity() {
	session.securityDeclined = true
	session.endpoint = session.initialEndpoint
	if session.protectedTCP != nil {
		_ = session.protectedTCP.Close()
		session.protectedTCP = nil
	}
	if session.protectedUDP != nil {
		_ = session.protectedUDP.Close()
		session.protectedUDP = nil
	}
	session.securityProposal = securityProposal{}
}

func (session *Session) activateIPSec(
	ctx context.Context,
	agreement securityAgreement,
	ck []byte,
	ik []byte,
) error {
	if session.securityActive {
		return errors.New("ims: ipsec-3gpp is already active")
	}
	if !session.securityOffered() {
		return ErrIPSecAgreementRequired
	}
	encryptionKey, integrityKey, err := expandIPSecKeys(ck, ik, agreement.selected.encryption, agreement.selected.algorithm)
	if err != nil {
		return err
	}
	defer zeroBytes(encryptionKey)
	defer zeroBytes(integrityKey)

	localIP := addressIP(session.conn.LocalAddr())
	remoteIP := addressIP(session.conn.RemoteAddr())
	if localIP == nil || remoteIP == nil {
		return errors.New("ims: protected SIP endpoints are unavailable")
	}
	selected := agreement.selected
	config := IPSecSAConfig{
		LocalIP:             localIP,
		RemoteIP:            remoteIP,
		IntegrityAlgorithm:  selected.algorithm,
		EncryptionAlgorithm: selected.encryption,
		UEClientSPI:         session.securityProposal.spiClient,
		UEServerSPI:         session.securityProposal.spiServer,
		PCSCFClientSPI:      selected.spiClient,
		PCSCFServerSPI:      selected.spiServer,
		UEClientPort:        session.securityProposal.portClient,
		UEServerPort:        session.securityProposal.portServer,
		PCSCFClientPort:     selected.portClient,
		PCSCFServerPort:     selected.portServer,
		EncryptionKey:       encryptionKey,
		IntegrityKey:        integrityKey,
	}
	handle, err := session.provider.installer.Install(ctx, config)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrIPSecInstall, err)
	}
	if handle == nil {
		return fmt.Errorf("%w: installer returned no handle", ErrIPSecInstall)
	}

	remoteAddress := net.JoinHostPort(remoteIP.String(), strconv.Itoa(selected.portServer))
	_ = session.conn.Close()
	connection, dialErr := dialSIP(
		ctx,
		session.transport,
		localIP.String(),
		session.securityProposal.portClient,
		remoteAddress,
	)
	if dialErr != nil {
		cleanupErr := handle.Close(context.Background())
		if cleanupErr != nil {
			return errors.Join(
				fmt.Errorf("ims: connect protected P-CSCF: %w", dialErr),
				fmt.Errorf("ims: roll back ipsec-3gpp: %w", cleanupErr),
			)
		}
		return fmt.Errorf("ims: connect protected P-CSCF: %w", dialErr)
	}

	session.conn = connection
	if session.transport == "tcp" {
		session.reader = bufio.NewReader(connection)
	} else {
		session.reader = nil
	}
	session.endpoint.port = selected.portServer
	session.securityAgreement = agreement
	session.securityActive = true
	session.ipsecHandle = handle
	return nil
}

func (session *Session) contactAddress() string {
	if session.securityOffered() {
		host := addressHost(session.conn.LocalAddr())
		return net.JoinHostPort(host, strconv.Itoa(session.securityProposal.portServer))
	}
	return session.conn.LocalAddr().String()
}

func (session *Session) emptyDigestAuthorization() string {
	uri := "sip:" + session.identity.domain
	return "Digest " + strings.Join([]string{
		`username="` + quoteDigest(session.identity.private) + `"`,
		`realm="` + quoteDigest(session.identity.domain) + `"`,
		`nonce=""`,
		`uri="` + quoteDigest(uri) + `"`,
		`response=""`,
		"algorithm=AKAv1-MD5",
		"integrity-protected=no",
	}, ", ")
}

func (session *Session) validProtectedUDPSource(remote *net.UDPAddr) bool {
	if remote == nil || !session.securityActive {
		return false
	}
	expectedIP := addressIP(session.conn.RemoteAddr())
	return expectedIP != nil && expectedIP.Equal(remote.IP)
}

func (session *Session) effectiveSecurityMode() string {
	if session.securityActive {
		return "ipsec-3gpp"
	}
	return "none"
}
