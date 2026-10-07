package device

import (
	"context"
	"errors"
	"fmt"
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
// the inserted card without changing profiles. With QMI configured, a missing
// eUICC permits one temporary AT channel open/close before a fresh inventory.
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

	entries, err := manager.esimInventoryOnce(ctx, id)
	if err != nil && manager.logger != nil {
		if errors.Is(err, ErrNoEUICC) {
			// 此时尚不能区分普通 SIM 与暂时检测失败，使用信息级别并保留首次读取详情。
			manager.logger.Info("eUICC inventory initial: not detected", "device_id", id, "error", HardwareErrorDetail(err))
		} else {
			manager.logger.Warn("eUICC inventory initial: read failed", "device_id", id, "error", HardwareErrorDetail(err))
		}
	}
	if !errors.Is(err, ErrNoEUICC) {
		return entries, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	state, lookupErr := manager.lookup(id)
	if lookupErr != nil {
		return nil, lookupErr
	}
	candidate := manager.candidateFor(state)
	// USB EC20 的 QMI 配置也进入此处，其 eSIM 实际走 AT+CSIM；不能只允许原生 QMI 设备。
	if !strings.EqualFold(manager.esimTransportFor(state), "qmi") || !candidate.HasATPort() {
		return entries, err
	}
	// 仍持有 UICC 锁；只探测本次分配的通道，不选择应用或重置 SIM。
	if probeErr := manager.probeATLogicalChannel(ctx, id); probeErr != nil {
		if manager.logger != nil {
			manager.logger.Warn("eUICC AT channel probe failed", "device_id", id, "error", HardwareErrorDetail(probeErr))
		}
		return nil, errors.Join(err, probeErr)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	entries, err = manager.esimInventoryOnce(ctx, id)
	if manager.logger != nil {
		if errors.Is(err, ErrNoEUICC) {
			// 普通 SIM 不支持 eUICC 是正常结果，不记录为硬件故障。
			manager.logger.Info("eUICC not detected after AT channel probe", "device_id", id, "error", HardwareErrorDetail(err))
		} else if err != nil {
			manager.logger.Warn("eUICC inventory retry after AT channel probe failed", "device_id", id, "error", HardwareErrorDetail(err))
		} else {
			manager.logger.Info("eUICC inventory recovered after AT channel probe", "device_id", id)
		}
	}
	return entries, err
}

// esimInventoryOnce requires the caller to hold the eSIM and UICC locks.
func (manager *Manager) esimInventoryOnce(ctx context.Context, id string) ([]EsimInventoryEntry, error) {
	aids, discoveryErr := manager.discoverEuiccAIDsWithErrors(ctx, id)
	entries := make([]EsimInventoryEntry, 0, len(aids))
	var lastErr error
	for _, aid := range aids {
		channel, err := manager.openEuiccAID(ctx, id, aid)
		if err != nil {
			lastErr = fmt.Errorf("esim: open application AID=%s: %w", aid, err)
			continue
		}
		profilePayload, profileErr := channel.es10(ctx, []byte{0xBF, 0x2D, 0x00})
		chip, chipErr := readEsimChipInfo(ctx, channel, aid)
		channel.close(context.Background())
		if profileErr != nil {
			lastErr = fmt.Errorf("esim: GetProfilesInfo AID=%s: %w", aid, profileErr)
			continue
		}
		if chipErr != nil {
			lastErr = fmt.Errorf("esim: read chip info AID=%s: %w", aid, chipErr)
			continue
		}
		info := EsimInfo{EID: chip.EID, AID: aid, Profiles: parseProfilesInfo(profilePayload)}
		entries = append(entries, EsimInventoryEntry{Info: info, Chip: chip})
	}
	if len(entries) == 0 {
		if lastErr != nil {
			if discoveryErr != nil {
				// 只补充已脱敏的诊断文本，保持 lastErr 原有的错误分类与恢复条件。
				return nil, fmt.Errorf("%w; discovery: %s", lastErr, HardwareErrorDetail(discoveryErr))
			}
			return nil, lastErr
		}
		return nil, ErrNoEUICC
	}
	return entries, nil
}
