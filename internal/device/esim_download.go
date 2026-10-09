package device

import (
	"context"
	"errors"
	"strings"
	"time"
)

// EsimDownloadParams are the SPA download form fields, mapped from the
// snake_case query params by the HTTP layer.
type EsimDownloadParams struct {
	SMDP             string
	MatchingID       string
	ConfirmationCode string
	AIDHex           string
	IMEI             string
}

// EsimProgress is one download step emitted to the SSE stream.
type EsimProgress struct {
	Step string
	Msg  string
	Pct  int
}

// EsimDownloadResult reports a completed install.
type EsimDownloadResult struct {
	ICCID      string
	SpaceDelta int64 // bytes consumed (positive)
	Warning    string
}

// ESIMDownloadProfile downloads and installs one eSIM profile (SGP.22 §3):
// challenge/info → ES9+ InitiateAuthentication → ES10b AuthenticateServer →
// ES9+ AuthenticateClient → ES10b PrepareDownload → ES9+ GetBoundProfilePackage
// → ES10b LoadBoundProfilePackage → ES9+ HandleNotification. progress is invoked
// with the SPA's expected step/pct sequence. The whole run holds the device's
// eSIM lock so a concurrent list/switch cannot disturb the card mid-install.
func (manager *Manager) ESIMDownloadProfile(ctx context.Context, id string, params EsimDownloadParams, progress func(EsimProgress)) (*EsimDownloadResult, error) {
	smdp := strings.TrimSpace(params.SMDP)
	if smdp == "" {
		return nil, errors.New("esim: SM-DP+ 地址不能为空")
	}
	report := func(step, msg string, pct int) {
		if progress != nil {
			progress(EsimProgress{Step: step, Msg: msg, Pct: pct})
		}
	}

	manager.lockESIM()
	defer manager.unlockESIM()

	report("preflight", "正在检查 eUICC 剩余空间...", 10)
	channel, err := manager.openEuiccAID(ctx, id, targetEuiccAID(params.AIDHex))
	if err != nil {
		return nil, err
	}
	defer channel.close(context.Background())

	// Free NVRAM before/after drives both the preflight check and space_delta.
	freeBefore := 0
	if info2, err := channel.getEUICCInfo2(ctx); err == nil {
		if n, ok := euiccFreeNVRAM(info2); ok {
			freeBefore = n
		}
	}

	challenge, err := channel.getEUICCChallenge(ctx)
	if err != nil {
		return nil, err
	}
	info1, err := channel.getEUICCInfo1(ctx)
	if err != nil {
		return nil, err
	}

	client, err := newES9PClient(ctx, smdp)
	if err != nil {
		return nil, err
	}

	report("auth_client", "正在向 SM-DP+ 进行客户端身份认证...", 30)
	init, err := client.initiateAuthentication(ctx, challenge, info1)
	if err != nil {
		return nil, err
	}
	transactionID := init.TransactionID
	transactionIDBytes := derFindValue(init.ServerSigned1, 0x80)

	// Best-effort session cleanup if anything fails after the transaction opens
	// (card-side CancelSession BF41, then server-side ES9+ cancelSession).
	finished := false
	defer func() {
		if !finished && len(transactionIDBytes) > 0 {
			if cancelResp, cerr := channel.cancelSession(context.Background(), transactionIDBytes, 0x00); cerr == nil {
				_ = client.cancelSession(context.Background(), transactionID, cancelResp)
			}
		}
	}()

	authResponse, err := channel.authenticateServer(ctx, init, params.MatchingID, params.IMEI)
	if err != nil {
		return nil, err
	}

	// A "cert not trusted"/"matchingID refused"/"EID mismatch" failure surfaces
	// here, from the SM-DP+'s functionExecutionStatus.
	auth, err := client.authenticateClient(ctx, transactionID, authResponse)
	if err != nil {
		return nil, err
	}

	report("download", "正在获取 Profile 数据包...", 55)
	prepareResponse, err := channel.prepareDownload(ctx, auth, params.ConfirmationCode)
	if err != nil {
		return nil, err
	}

	bpp, err := client.getBoundProfilePackage(ctx, transactionID, prepareResponse)
	if err != nil {
		return nil, err
	}

	report("install", "正在将 Profile 写入 eUICC...", 80)
	installResponse, err := channel.loadBoundProfilePackage(ctx, bpp, func(done, total int) {
		if total > 0 {
			report("install", "正在将 Profile 写入 eUICC...", 80+done*8/total)
		}
	})
	if err != nil {
		return nil, err
	}
	report("notify", "正在向运营商发送下载通知...", 90)
	iccid, installErr := installationResult(installResponse)
	warning := ""
	notification, notificationErr := parsePendingNotification(installResponse)
	if notificationErr == nil {
		// Loading the final BPP segment is the commit point. Finish the operator
		// acknowledgement even if the browser closes its SSE connection now.
		notifyContext, cancelNotify := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		notificationErr = channel.deliverNotification(notifyContext, notification)
		cancelNotify()
	}
	if notificationErr != nil {
		warning = "Profile 安装结果已保留在 eUICC，但向运营商上报失败，可在当前通知列表中重发"
	}
	// Error installation results must be reported too. Return the card-side
	// installation failure only after making that best-effort ES9+ attempt.
	if installErr != nil {
		return nil, installErr
	}

	freeAfter := freeBefore
	if info2, err := channel.getEUICCInfo2(ctx); err == nil {
		if n, ok := euiccFreeNVRAM(info2); ok {
			freeAfter = n
		}
	}
	spaceDelta := freeBefore - freeAfter
	if spaceDelta <= 0 {
		spaceDelta = len(bpp) // fall back to the package size when NVRAM unreadable
	}

	// The HTTP layer owns the final "done" event (it attaches space_delta/warning).
	finished = true
	return &EsimDownloadResult{ICCID: iccid, SpaceDelta: int64(spaceDelta), Warning: warning}, nil
}

// ESIMDownloadErrorCode maps a download failure to a stable SPA error code.
// Keep the matching deliberately tolerant because some SM-DP+ implementations
// return only a free-form statusCodeData.message.
func ESIMDownloadErrorCode(err error) string {
	var authenticateErr *esimAuthenticateError
	if errors.As(err, &authenticateErr) {
		return "euicc_authentication_failed"
	}
	var es9pErr *es9pError
	if errors.As(err, &es9pErr) {
		switch {
		case es9pErr.SubjectCode == "8.1" && es9pErr.ReasonCode == "4.8":
			return "euicc_insufficient_memory"
		case es9pErr.SubjectCode == "8.8.4" && es9pErr.ReasonCode == "3.7":
			return "euicc_ci_incompatible"
		case es9pErr.SubjectCode == "8.2.6" && es9pErr.ReasonCode == "3.8":
			return "activation_code_refused"
		case es9pErr.SubjectCode == "8.2.5" && es9pErr.ReasonCode == "3.7":
			return "profile_pool_empty"
		}
	}
	var installErr *esimInstallError
	if errors.As(err, &installErr) && installErr.ErrorReason == 10 {
		return "euicc_insufficient_memory"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "insufficient") || strings.Contains(lower, "空间不足") {
		return "euicc_insufficient_memory"
	}
	if strings.Contains(lower, "cert.dpauth") &&
		(strings.Contains(lower, "root ca") || strings.Contains(lower, "public key supported by the euicc")) {
		return "euicc_ci_incompatible"
	}
	if strings.Contains(lower, "campaign resource pool is empty") ||
		strings.Contains(lower, "no more profile available") {
		return "profile_pool_empty"
	}
	if strings.Contains(lower, "matchingid") && strings.Contains(lower, "refused") || lower == "refused" {
		return "activation_code_refused"
	}
	return "download_failed"
}

// EsimChipInfo describes the eUICC for the SPA's eSIM chip header.
type EsimChipInfo struct {
	EID                string
	AID                string
	FreeNvramBytes     int
	HasFreeNvram       bool
	TrustedCIs         []string // raw hex SubjectKeyIdentifiers
	Certificates       []string // friendly CI names (证书)
	FirmwareVer        string   // euiccFirmwareVer (固件)
	Manufacturer       string   // EUM issuer → 生产商
	DefaultSmdpAddress string   // ES10a default SM-DP+
	RootDsAddress      string   // ES10a Root SM-DS
	SAS                string   // sasAccreditationNumber
}

// ESIMChipInfo reads the eUICC's EID, EUICCInfo2, and configured addresses for
// the chip header. It takes the eSIM lock like the other card ops.
func (manager *Manager) ESIMChipInfo(ctx context.Context, id string) (*EsimChipInfo, error) {
	manager.lockESIM()
	defer manager.unlockESIM()

	var lastErr error
	for _, aid := range manager.discoverEuiccAIDs(ctx, id) {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			lastErr = err
			continue
		}
		info, err := readEsimChipInfo(ctx, channel, aid)
		channel.close(context.Background())
		if err != nil {
			lastErr = err
			continue
		}
		return &info, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrNoEUICC
}

func readEsimChipInfo(ctx context.Context, channel *euiccChannel, aidHex string) (EsimChipInfo, error) {
	info := EsimChipInfo{AID: aidHex}
	if eid, err := channel.getEID(ctx); err == nil {
		info.EID = eid
		info.Manufacturer = eumManufacturerForEID(eid)
	}
	if info2, err := channel.getEUICCInfo2(ctx); err == nil {
		if n, ok := euiccFreeNVRAM(info2); ok {
			info.FreeNvramBytes = n
			info.HasFreeNvram = true
		}
		info.TrustedCIs = euiccTrustedCIs(info2)
		info.FirmwareVer = euiccFirmwareVersion(info2)
		info.SAS = euiccSAS(info2)
		for _, hexID := range info.TrustedCIs {
			info.Certificates = append(info.Certificates, ciKeyFriendlyName(hexID))
		}
	}
	if def, root := channel.getEuiccConfiguredAddresses(ctx); def != "" || root != "" {
		info.DefaultSmdpAddress = def
		info.RootDsAddress = root
	}
	// Report whatever we read (even partial); only a channel-open failure above
	// is fatal. A wholly-empty result means the eUICC exposed nothing usable.
	if info.EID == "" && !info.HasFreeNvram && len(info.TrustedCIs) == 0 {
		return EsimChipInfo{}, errors.New("esim: eUICC did not report chip info")
	}
	return info, nil
}

// ESIMInventory reads every independently addressable eUICC storage exposed by
// the inserted card. It is entirely read-only: only SELECT, GetProfilesInfo,
// GetEuiccData, GetEuiccInfo2 and GetEuiccConfiguredAddresses are issued.
func (manager *Manager) ESIMInventory(ctx context.Context, id string) ([]EsimInventoryEntry, error) {
	ctx, cancel := boundESIMContext(ctx)
	defer cancel()
	if err := manager.lockESIMContext(ctx); err != nil {
		return nil, err
	}
	defer manager.unlockESIM()
	if manager.esimRecoveryActive(id) {
		return nil, errESIMRecovering
	}

	aids := manager.discoverEuiccAIDs(ctx, id)
	entries := make([]EsimInventoryEntry, 0, len(aids))
	var lastErr error
	for _, aid := range aids {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			lastErr = err
			continue
		}
		profilePayload, profileErr := channel.es10(ctx, []byte{0xBF, 0x2D, 0x00})
		chip, chipErr := readEsimChipInfo(ctx, channel, aid)
		channel.close(context.Background())
		if profileErr != nil {
			lastErr = profileErr
			continue
		}
		if chipErr != nil {
			lastErr = chipErr
			continue
		}
		info := EsimInfo{EID: chip.EID, AID: aid, Profiles: parseProfilesInfo(profilePayload)}
		entries = append(entries, EsimInventoryEntry{Info: info, Chip: chip})
	}
	if len(entries) == 0 {
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, ErrNoEUICC
	}
	return entries, nil
}
