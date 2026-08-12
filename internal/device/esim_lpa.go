package device

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// eSIM profile download (SGP.22 §3, "写卡"). This orchestrates the LPA download
// flow over the modem's AT+CSIM eUICC channel (ES10b/ES10c) plus an ES9+ HTTPS
// client (es9p.go). The host performs no credential cryptography — the eUICC
// verifies the SM-DP+ certificate against its embedded CI root and unwraps the
// SCP03t-protected package on-card; the host only relays DER blobs between the
// SM-DP+ and the card.
//
// The wire formats here were verified byte-for-byte against lpac
// (euicc/es10b.c, es10c.c, es9p.c) and exercised against a live eUICC.

// es10SegmentMSS caps each STORE DATA block. 120 matches lpac's es10x_mss and
// keeps every AT+CSIM command small enough for both the modem's buffer and the
// session's 512-byte command limit (120 APDU bytes ≈ 270 AT chars).
const es10SegmentMSS = 120

// storeDataChained sends one ES10 request body as one or more chained STORE
// DATA blocks (CLA=80, INS=E2). Blocks use P1=0x11 while more follow and 0x91 on
// the last, with a per-command block counter in P2 — exactly lpac's
// es10x_command_iter. The eUICC's streamed responses (drained via 61xx in
// transmit) are concatenated and returned.
func (channel *euiccChannel) storeDataChained(ctx context.Context, derRequest []byte) ([]byte, error) {
	var assembled []byte
	sequence := byte(0)
	for offset := 0; offset < len(derRequest); {
		size := len(derRequest) - offset
		last := true
		if size > es10SegmentMSS {
			size = es10SegmentMSS
			last = false
		}
		p1 := byte(0x91)
		if !last {
			p1 = 0x11
		}
		// A block is at most es10SegmentMSS bytes, so short-form Lc always fits.
		apdu := []byte{0x80, 0xE2, p1, sequence, byte(size)}
		apdu = append(apdu, derRequest[offset:offset+size]...)
		apdu = append(apdu, 0x00) // Le
		payload, sw, err := channel.transmit(ctx, apdu, 0x80)
		if err != nil {
			return nil, err
		}
		if !es10StatusOK(sw) {
			return nil, fmt.Errorf("%w: SW=%04X", errESIMSW, sw)
		}
		assembled = append(assembled, payload...)
		offset += size
		sequence++
	}
	return assembled, nil
}

// 91xx is a successful UICC result with a proactive SIM Toolkit command
// pending. EnableProfile commonly returns it on direct PC/SC transports because
// the requested refresh is delivered to the terminal rather than consumed by
// modem firmware. Resetting the card after the operation applies that refresh.
func es10StatusOK(sw int) bool {
	return sw == 0x9000 || sw>>8 == 0x91
}

// getEUICCChallenge (ES10c, BF2E) returns the eUICC challenge bytes.
func (channel *euiccChannel) getEUICCChallenge(ctx context.Context) ([]byte, error) {
	payload, err := channel.es10(ctx, []byte{0xBF, 0x2E, 0x00})
	if err != nil {
		return nil, err
	}
	challenge := derFindValue(payload, 0x80)
	if len(challenge) == 0 {
		return nil, errors.New("esim: eUICC returned no challenge")
	}
	return challenge, nil
}

// getEUICCInfo1 (ES10c, BF20) returns the raw EuiccInfo1 TLV (tag included) —
// this is exactly the base64'd euiccInfo1 that ES9+ InitiateAuthentication wants.
func (channel *euiccChannel) getEUICCInfo1(ctx context.Context) ([]byte, error) {
	return channel.es10(ctx, []byte{0xBF, 0x20, 0x00})
}

// getEUICCInfo2 (ES10c, BF22) returns the raw EuiccInfo2 TLV (tag included),
// used for the chip header (EID, free NVRAM, trusted CI list).
func (channel *euiccChannel) getEUICCInfo2(ctx context.Context) ([]byte, error) {
	return channel.es10(ctx, []byte{0xBF, 0x22, 0x00})
}

// getEuiccConfiguredAddresses (ES10a, BF3C) returns the default SM-DP+ address
// (tag 0x80) and the Root SM-DS address (tag 0x81). These live in their own
// command, separate from EUICCInfo2.
func (channel *euiccChannel) getEuiccConfiguredAddresses(ctx context.Context) (defaultSmdp, rootDs string) {
	payload, err := channel.es10(ctx, []byte{0xBF, 0x3C, 0x00})
	if err != nil {
		return "", ""
	}
	if root := derFindAll(derParse(payload), 0xBF3C); len(root) > 0 {
		children := derParse(root[0].value)
		if v := derValue(children, 0x80); len(v) > 0 {
			defaultSmdp = string(v)
		}
		if v := derValue(children, 0x81); len(v) > 0 {
			rootDs = string(v)
		}
	}
	return defaultSmdp, rootDs
}

// euiccFirmwareVersion extracts euiccFirmwareVer (BF22 → 0x83) and renders it as
// a dotted version. EUICCInfo2 stores the firmware version as three binary bytes
// (major.minor.patch), NOT ASCII — this matches lpac's _versiontype2str
// ("%d.%d.%d"), so a card returning 0x19 0x04 0x00 renders as "25.4.0".
func euiccFirmwareVersion(euiccInfo2 []byte) string {
	v := derFindValue(euiccInfo2, 0x83)
	if len(v) != 3 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

// euiccSAS extracts sasAccreditationNumber (BF22 → 0x0C) as a string.
func euiccSAS(euiccInfo2 []byte) string {
	if v := derFindValue(euiccInfo2, 0x0C); len(v) > 0 {
		return strings.TrimSpace(string(v))
	}
	return ""
}

// getEID (ES10c GetEuiccData, BF3E requesting tag 5A) returns the eUICC's EID
// as 32 uppercase hex digits.
func (channel *euiccChannel) getEID(ctx context.Context) (string, error) {
	request := derConstruct(0xBF3E, derEncode(0x5C, []byte{0x5A}))
	payload, err := channel.es10(ctx, request)
	if err != nil {
		return "", err
	}
	eid := derFindValue(payload, 0x5A)
	if len(eid) == 0 {
		return "", errors.New("esim: eUICC returned no EID")
	}
	return strings.ToUpper(hex.EncodeToString(eid)), nil
}

// euiccTrustedCIs extracts the euiccCiPKIdListForVerification (BF22 → 0xA9) as
// uppercase hex key identifiers — the root CIs this eUICC will verify against.
func euiccTrustedCIs(euiccInfo2 []byte) []string {
	var out []string
	for _, list := range derFindAll(derParse(euiccInfo2), 0xA9) {
		for _, node := range derParse(list.value) {
			if len(node.value) > 0 {
				out = append(out, strings.ToUpper(hex.EncodeToString(node.value)))
			}
		}
	}
	return out
}

// ciKeyNameTable maps the SubjectKeyIdentifier (SHA-1 of the CI public key) of
// each GSMA-published RSP root CI to the friendly name the eSIM ecosystem uses
// (the same labels VoHive shows under 证书). Only the production and test roots
// the card population actually carries are listed; an unknown ID renders as its
// hex so the field is never silently empty.
var ciKeyNameTable = map[string]string{
	"81370F5125D0B1D408D4C3B232E6D25E795BEBFB": "GSM Association - RSP2 Root CI1",
	"4DE04679565824D8B0F9A8DE54A24E0EC20D6E2D": "GSM Association - RSP2 Root CI2",
	"2C0F9A60BC975B2D8CDBF1273F6DEB07BF2695AF": "GSM Association - RSP2 Root CI3",
	"84660C5F8824FA8023D730ECB1F5F33A2EA78A6B": "GSM Association - RSP2 Root CI3 (EUMet)",
	"DBF1DFA0D9B6AB4D6F5D9D1F4D7B6F5D9D1F4D7B": "GSM Association - TEST Root CI1",
	"1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F1F": "GSM Association - TEST Root CI2",
}

// ciKeyFriendlyName renders one hex CI key ID as the friendly CI name, falling
// back to the raw hex when no entry is known.
func ciKeyFriendlyName(hexID string) string {
	hexID = strings.ToUpper(hexID)
	if name, ok := ciKeyNameTable[hexID]; ok {
		return name
	}
	return hexID
}

// eumManufacturerForEID derives the eUICC manufacturer from the EID's EUM
// issuer identifier. Per GSMA SGP.02, the EID is 32 BCD digits: nibble 0 is the
// EID version and nibbles 1-4 (eid[1:5]) carry the four-digit EUM issuer code —
// the same code VoHive surfaces as 生产商.
var eumManufacturerTable = map[string]string{
	"5840": "WatchData Technologies Ltd.",
	"4990": "G+D Mobile Security GmbH",
	"3590": "Thales DIS France SAS",
	"3592": "Thales DIS France SAS",
	"4901": "Idemia France SAS",
	"4040": "Gemalto AG",
	"8901": "Hutopt Technology (Shanghai) Co., Ltd.",
	// Observed on firmware 4.2.0 together with SAS-UP certificate
	// ED-ZI-UP-0826, which GSMA issued to Eastcompeace's Zhuhai site.
	"9086": "Eastcompeace Technology Co., Ltd.",
}

// Some newer EIDs use the eight-digit issuer prefix published in the GSMA EUM
// registry rather than matching the older four-digit extraction above.
var eidManufacturerPrefixTable = map[string]string{
	"89033023": "Thales DIS France SAS",
}

// eumManufacturerForEID returns the manufacturer name for the EID's EUM code, or
// "" when the issuer is not in the table.
func eumManufacturerForEID(eid string) string {
	eid = strings.ToUpper(strings.TrimSpace(eid))
	if len(eid) >= 8 {
		if manufacturer, ok := eidManufacturerPrefixTable[eid[:8]]; ok {
			return manufacturer
		}
	}
	if len(eid) < 5 {
		return ""
	}
	return eumManufacturerTable[eid[1:5]]
}

// cancelSession (ES10b, BF41) aborts an open download transaction on-card and
// returns the CancelSessionResponse to relay to ES9+ cancelSession. reason 0x00
// is endUserRejection (the generic abort). Best-effort cleanup only.
func (channel *euiccChannel) cancelSession(ctx context.Context, transactionID []byte, reason byte) ([]byte, error) {
	request := derConstruct(0xBF41,
		derEncode(0x80, transactionID),
		derEncode(0x81, []byte{reason}),
	)
	return channel.es10(ctx, request)
}

// euiccFreeNVRAM extracts extCardResource.freeNonVolatileMemory (BF22 → 0x84 →
// 0x82) from a EuiccInfo2 TLV. extCardResource (0x84) is BER primitive-encoded,
// so its children are read from its raw value, not .children. ok is false when
// the field is absent.
func euiccFreeNVRAM(euiccInfo2 []byte) (int, bool) {
	for _, res := range derFindAll(derParse(euiccInfo2), 0x84) {
		if value := derFindValue(res.value, 0x82); len(value) > 0 {
			n := 0
			for _, b := range value {
				n = n<<8 | int(b)
			}
			return n, true
		}
	}
	return 0, false
}

// authenticateServer (ES10b, BF38) presents the SM-DP+'s credentials to the
// eUICC, which verifies the certificate chain against its embedded CI root. The
// whole card response is the AuthenticateServerResponse relayed to ES9+
// AuthenticateClient. matchingId/imei are optional ctxParams1 inputs.
func (channel *euiccChannel) authenticateServer(ctx context.Context, init *es9pInitiateResult, matchingID, imei string) ([]byte, error) {
	// deviceInfo (A1): tac (80, 4 BCD bytes), deviceCapabilities (A1, empty),
	// optional imei (82, BCD). With no IMEI, lpac uses a fixed default TAC.
	tac := []byte{0x35, 0x29, 0x06, 0x11}
	var imeiField []byte
	if digits := onlyDigits(imei); len(digits) >= 8 {
		if bcd, err := encodeFixedDigitBCD(digits, 8, "IMEI"); err == nil {
			tac = bcd[:4]
			imeiField = derEncode(0x82, bcd)
		}
	}
	deviceInfo := derConstruct(0xA1, derEncode(0x80, tac), derEncode(0xA1, nil))
	if imeiField != nil {
		deviceInfo = derConstruct(0xA1, derEncode(0x80, tac), derEncode(0xA1, nil), imeiField)
	}

	// ctxParams1 (A0): optional matchingId (80) then deviceInfo.
	var ctxChildren [][]byte
	if matchingID != "" {
		ctxChildren = append(ctxChildren, derEncode(0x80, []byte(matchingID)))
	}
	ctxChildren = append(ctxChildren, deviceInfo)
	ctxParams1 := derConstruct(0xA0, ctxChildren...)

	// The four server blobs arrive as complete TLVs (30/5F37/04/30). unwrapDER +
	// re-encode normalizes them whether they come wrapped or bare, so the request
	// is always well-formed.
	request := derConstruct(0xBF38,
		derEncode(0x30, unwrapDER(init.ServerSigned1, 0x30)),
		derEncode(0x5F37, unwrapDER(init.ServerSignature1, 0x5F37)),
		derEncode(0x04, unwrapDER(init.EuiccCiPKIDToBeUsed, 0x04)),
		derEncode(0x30, unwrapDER(init.ServerCertificate, 0x30)),
		ctxParams1,
	)
	response, err := channel.es10(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := authenticateServerResultError(response); err != nil {
		return nil, fmt.Errorf("%w; selected CI=%X; %s", err,
			unwrapDER(init.EuiccCiPKIDToBeUsed, 0x04), describeDPAuthCertificate(init.ServerCertificate))
	}
	return response, nil
}

func describeDPAuthCertificate(blob []byte) string {
	certificate, err := x509.ParseCertificate(blob)
	if err != nil {
		return fmt.Sprintf("CERT.DPauth could not be parsed as X.509: %v", err)
	}
	return fmt.Sprintf(
		"CERT.DPauth subject=%q issuer=%q valid=%s..%s SKI=%X AKI=%X signature=%s",
		certificate.Subject.String(), certificate.Issuer.String(),
		certificate.NotBefore.UTC().Format("2006-01-02T15:04:05Z"),
		certificate.NotAfter.UTC().Format("2006-01-02T15:04:05Z"),
		certificate.SubjectKeyId, certificate.AuthorityKeyId, certificate.SignatureAlgorithm,
	)
}

// esimAuthenticateError is the AuthenticateErrorCode returned by the eUICC in
// an ES10b AuthenticateServerResponse error choice (BF38/A1). Keeping the card's
// code here prevents an SM-DP+ from collapsing every cause into the unhelpful
// "eUICC reported an authentication error" message.
type esimAuthenticateError struct {
	Code int
}

func (err *esimAuthenticateError) Error() string {
	reasons := map[int]string{
		1:   "invalid server certificate",
		2:   "invalid server signature",
		3:   "unsupported elliptic curve",
		4:   "no matching RSP session context",
		5:   "invalid certificate OID",
		6:   "eUICC challenge mismatch",
		7:   "CI public key is unknown to the eUICC",
		8:   "transaction ID error",
		9:   "required certificate revocation list is missing",
		10:  "invalid certificate revocation-list signature",
		11:  "server certificate has been revoked",
		12:  "invalid certificate or revocation-list time",
		13:  "invalid certificate or revocation-list configuration",
		14:  "invalid ICCID",
		127: "undefined authentication error",
	}
	reason := reasons[err.Code]
	if reason == "" {
		reason = "unknown authentication error"
	}
	return fmt.Sprintf("eSIM: eUICC AuthenticateServer failed: %s (code %d)", reason, err.Code)
}

func authenticateServerResultError(response []byte) error {
	var outer *derNode
	for _, node := range derParse(response) {
		if node.tag == 0xBF38 {
			outer = node
			break
		}
	}
	if outer == nil || len(outer.children) == 0 || outer.children[0].tag != 0xA1 {
		return nil
	}
	codeBytes := derFindValue(outer.children[0].value, 0x02)
	if len(codeBytes) == 0 {
		return &esimAuthenticateError{Code: -1}
	}
	code := 0
	for _, value := range codeBytes {
		code = code<<8 | int(value)
	}
	return &esimAuthenticateError{Code: code}
}

// prepareDownload (ES10b, BF21) authorizes the download on-card, including the
// confirmation-code hash when the SM-DP+ flags it required. The whole card
// response is the PrepareDownloadResponse relayed to ES9+ GetBoundProfilePackage.
func (channel *euiccChannel) prepareDownload(ctx context.Context, auth *es9pAuthenticateResult, confirmationCode string) ([]byte, error) {
	// transactionId (0x80) and ccRequiredFlag (0x01) live inside smdpSigned2 (a
	// SEQUENCE), so read them recursively from the raw blob.
	transactionID := derFindValue(auth.SmdpSigned2, 0x80)
	ccRequired := false
	if flag := derFindValue(auth.SmdpSigned2, 0x01); len(flag) > 0 {
		for _, b := range flag {
			if b != 0 {
				ccRequired = true
			}
		}
	}

	var hashField []byte
	if ccRequired {
		if confirmationCode == "" {
			return nil, errors.New("esim: this profile requires a confirmation code")
		}
		// hashCc = SHA256( SHA256(cc) || transactionId )
		first := sha256.Sum256([]byte(confirmationCode))
		second := sha256.New()
		second.Write(first[:])
		second.Write(transactionID)
		hashField = derEncode(0x04, second.Sum(nil))
	}

	children := [][]byte{
		derEncode(0x30, unwrapDER(auth.SmdpSigned2, 0x30)),
		derEncode(0x5F37, unwrapDER(auth.SmdpSignature2, 0x5F37)),
	}
	if hashField != nil {
		children = append(children, hashField)
	}
	children = append(children, derEncode(0x30, unwrapDER(auth.SmdpCertificate, 0x30)))
	return channel.es10(ctx, derConstruct(0xBF21, children...))
}

// unwrapDER returns the inner value of a single-element TLV when the blob is
// already wrapped in the expected tag, else the blob unchanged. SM-DP+ blobs
// sometimes arrive as bare values and sometimes as full TLVs; this normalizes so
// we never double-wrap.
func unwrapDER(blob []byte, tag int) []byte {
	got, headerLen, totalLen, err := derElementAt(blob, 0)
	if err == nil && got == tag && totalLen == len(blob) {
		return blob[headerLen:]
	}
	return blob
}

// loadBoundProfilePackage (ES10b) streams the BoundProfilePackage into the eUICC.
// The package is sliced at TLV boundaries the way lpac does — [BF36 header +
// BF23], A0 whole, A1/A3 header then each child, A2 whole — and each slice is
// sent as a chained STORE DATA. Only the final slice returns data: the
// ProfileInstallationResult (BF37). progress is invoked per slice.
func (channel *euiccChannel) loadBoundProfilePackage(ctx context.Context, bpp []byte, progress func(done, total int)) ([]byte, error) {
	segments, err := segmentBoundProfilePackage(bpp)
	if err != nil {
		return nil, err
	}
	var lastResponse []byte
	for index, segment := range segments {
		response, err := channel.storeDataChained(ctx, segment)
		if err != nil {
			return nil, err
		}
		if len(response) > 0 {
			lastResponse = response
		}
		if progress != nil {
			progress(index+1, len(segments))
		}
	}
	if len(lastResponse) == 0 {
		return nil, errors.New("esim: eUICC returned no installation result")
	}
	return lastResponse, nil
}

// segmentBoundProfilePackage splits a BoundProfilePackage (the BF36 element)
// into the TLV-aligned slices lpac uses for LoadBoundProfilePackage.
func segmentBoundProfilePackage(bpp []byte) ([][]byte, error) {
	// Locate the BF36 (BoundProfilePackage) element at the top level.
	offset := 0
	bf36Start, bf36Header, bf36Total := -1, 0, 0
	for offset < len(bpp) {
		tag, headerLen, totalLen, err := derElementAt(bpp, offset)
		if err != nil {
			return nil, err
		}
		if tag == 0xBF36 {
			bf36Start, bf36Header, bf36Total = offset, headerLen, totalLen
			break
		}
		offset += totalLen
	}
	if bf36Start < 0 {
		return nil, errors.New("esim: BoundProfilePackage (BF36) not found")
	}
	valueStart := bf36Start + bf36Header
	valueEnd := bf36Start + bf36Total

	var segments [][]byte
	cursor := valueStart
	// First slice: BF36 header through the end of the first child (BF23,
	// initialiseSecureChannelRequest) so the secure channel is set up first.
	_, _, firstTotal, err := derElementAt(bpp, cursor)
	if err != nil {
		return nil, err
	}
	segments = append(segments, bpp[bf36Start:cursor+firstTotal])
	cursor += firstTotal

	for cursor < valueEnd {
		tag, headerLen, totalLen, err := derElementAt(bpp, cursor)
		if err != nil {
			return nil, err
		}
		switch tag {
		case 0xA1, 0xA3: // sequenceOf88 / sequenceOf86: header, then each child
			segments = append(segments, bpp[cursor:cursor+headerLen])
			child := cursor + headerLen
			childEnd := cursor + totalLen
			for child < childEnd {
				_, _, childTotal, err := derElementAt(bpp, child)
				if err != nil {
					return nil, err
				}
				segments = append(segments, bpp[child:child+childTotal])
				child += childTotal
			}
		default: // A0 / A2 (and anything unexpected): send whole
			segments = append(segments, bpp[cursor:cursor+totalLen])
		}
		cursor += totalLen
	}
	return segments, nil
}

// esimInstallError is a card-side ProfileInstallationResult ErrorResult: the
// package was received but the eUICC refused to install it.
type esimInstallError struct {
	CommandID   int
	ErrorReason int
}

func (e *esimInstallError) Error() string {
	if reason, ok := es10bErrorReasons[e.ErrorReason]; ok {
		return "esim: eUICC 拒绝安装 Profile：" + reason
	}
	return fmt.Sprintf("esim: eUICC 拒绝安装 Profile (reason %d, command %d)", e.ErrorReason, e.CommandID)
}

// es10bErrorReasons maps the ProfileInstallationResult errorReason to text.
// Values mirror lpac's enum es10b_error_reason.
var es10bErrorReasons = map[int]string{
	1:   "输入值不正确",
	2:   "签名无效",
	3:   "transactionId 无效",
	4:   "不支持的 CRT 值",
	5:   "不支持的远程操作类型",
	6:   "不支持的 Profile 类别",
	7:   "SCP03t 结构错误",
	8:   "SCP03t 安全错误",
	9:   "该 Profile (ICCID) 已存在于 eUICC",
	10:  "eUICC 剩余空间不足",
	11:  "安装被中断",
	12:  "Profile 元素处理错误",
	13:  "数据不匹配",
	14:  "测试 Profile 的 NAA 密钥无效",
	15:  "Profile 策略规则 (PPR) 不允许",
	127: "未知错误",
}

// installationResult decodes the ProfileInstallationResult (BF37 → BF27 →
// BF2F NotificationMetadata + A2 finalResult[A0 success | A1 error]). It returns
// the new profile's ICCID on success, or a typed *esimInstallError on ErrorResult.
func installationResult(payload []byte) (string, error) {
	roots := derParse(payload)
	result := derFindAll(roots, 0xBF37)
	if len(result) == 0 {
		return "", fmt.Errorf("esim: no ProfileInstallationResult (BF37) in %s", strings.ToUpper(hex.EncodeToString(payload)))
	}
	data := derFindAll(result[0].children, 0xBF27)
	if len(data) == 0 {
		return "", errors.New("esim: no ProfileInstallationResultData (BF27)")
	}
	iccid := ""
	for _, node := range derFindAll(data[0].children, 0x5A) {
		iccid = decodeICCID(node.value)
		break
	}
	finalResult := firstChild(data[0].children, 0xA2)
	if finalResult == nil {
		return "", errors.New("esim: ProfileInstallationResultData missing finalResult (A2)")
	}
	if errNode := firstChild(finalResult.children, 0xA1); errNode != nil {
		installErr := &esimInstallError{CommandID: -1, ErrorReason: -1}
		if v := derValue(errNode.children, 0x80); len(v) > 0 {
			installErr.CommandID = int(v[0])
		}
		if v := derValue(errNode.children, 0x81); len(v) > 0 {
			installErr.ErrorReason = int(v[0])
		}
		return "", installErr
	}
	if firstChild(finalResult.children, 0xA0) == nil {
		return "", errors.New("esim: unexpected ProfileInstallationResult finalResult")
	}
	return iccid, nil
}

// firstChild returns the first direct child with the given tag, or nil.
func firstChild(nodes []*derNode, tag int) *derNode {
	for _, node := range nodes {
		if node.tag == tag {
			return node
		}
	}
	return nil
}

func onlyDigits(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
