import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { ArrowSyncRegular, AddRegular, ChevronLeftRegular } from "@fluentui/react-icons";
import { api, apiMessage, camelize } from "../api";
import type { CardPolicy, DeviceConfig, DeviceListItem, DiscoveredDevice } from "../types";
import { usePolling } from "../lib/usePolling";
import { useMediaQuery } from "../lib/useMediaQuery";
import { Button, PageHeader, RefreshButton, ErrorState, ListSkeleton, Tabs, confirmDialog, message } from "../components/ui";
import { DeviceListPanel, type StatusFilter, type SortDir, type SortKey } from "../components/devices/DeviceListPanel";
import { DeviceDetailHeader } from "../components/devices/DeviceDetailHeader";
import { DeviceOverviewTab } from "../components/devices/DeviceOverviewTab";
import { DeviceEsimTab } from "../components/devices/DeviceEsimTab";
import { DeviceAtTab } from "../components/devices/DeviceAtTab";
import { DeviceUssdTab } from "../components/devices/DeviceUssdTab";
import { DeviceConfigTab } from "../components/devices/DeviceConfigTab";
import { CardPolicyPanel } from "../components/devices/CardPolicyPanel";
import { DeviceAddDialog } from "../components/devices/DeviceAddDialog";
import { CarrierWebsheetDialog, type CarrierWebsheet } from "../components/devices/CarrierWebsheetDialog";
import { copyText, isDeviceOnline, isQmiControl, isRecoveringPhase, readEventStream, simOperatorDisplay } from "../components/devices/shared";
import type { AddDeviceForm, DeviceDetail, LoadError } from "../components/devices/types";
import { tf, useI18n } from "../lib/i18n";

const VALID_TABS = new Set(["overview", "esim", "at", "ussd", "config", "card"]);
const EMPTY_ADD: AddDeviceForm = {
  id: "",
  name: "",
  deviceType: "",
  interface: "",
  modemImei: "",
  usbPath: "",
  esimTransport: "at",
  atPort: "",
  controlDevice: "",
  deviceBackend: "at",
	 simPin: "",
};

export default function DevicesPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [list, setList] = useState<DeviceListItem[]>([]);
  const [deviceLimit, setDeviceLimit] = useState(0);
  const [listLoading, setListLoading] = useState(true);
  const [listError, setListError] = useState<LoadError | null>(null);
  const [lastOkAt, setLastOkAt] = useState<number | null>(null);
  const [detail, setDetail] = useState<DeviceDetail | null>(null);
  const [selectedId, setSelectedId] = useState("");
  const [activeTab, setActiveTab] = useState("overview");
  const [editConfig, setEditConfig] = useState<DeviceConfig | null>(null);
  const [configSnapshot, setConfigSnapshot] = useState("");
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
	const [dataToggling, setDataToggling] = useState(false);
  const [rebooting, setRebooting] = useState(false);
  const [reconnectingVoWiFi, setReconnectingVoWiFi] = useState(false);
  const [rescanning, setRescanning] = useState(false);
  const [e911Starting, setE911Starting] = useState(false);
  const [discovered, setDiscovered] = useState<DiscoveredDevice[]>([]);
  const [discovering, setDiscovering] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [addSelected, setAddSelected] = useState<DiscoveredDevice | null>(null);
  const [addConfig, setAddConfig] = useState<AddDeviceForm>(EMPTY_ADD);
  const [addSaving, setAddSaving] = useState(false);
  const [websheet, setWebsheet] = useState<CarrierWebsheet | null>(null);
  const [websheetOpen, setWebsheetOpen] = useState(false);
  const [cardPolicy, setCardPolicy] = useState<CardPolicy | null>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [sortKey, setSortKey] = useState<SortKey>("name");
  const [sortDir, setSortDir] = useState<SortDir>("asc");

  const selectedIdRef = useRef(selectedId);
  selectedIdRef.current = selectedId;
  const detailRef = useRef(detail);
  detailRef.current = detail;
  const activeTabRef = useRef(activeTab);
  activeTabRef.current = activeTab;
  const searchParamsRef = useRef(searchParams);
  searchParamsRef.current = searchParams;

  const isMobile = useMediaQuery("(max-width: 767px)");

  const handleBackToList = useCallback(() => {
    setSelectedId("");
    const p = new URLSearchParams(searchParamsRef.current);
    p.delete("device");
    p.delete("tab");
    setSearchParams(p, { replace: true });
    setDetail(null);
  }, [setSearchParams]);

  const loadDetail = useCallback(async (id: string) => {
    if (!id) {
      setDetail(null);
      return;
    }
    try {
      const res = await api<{ devices?: DeviceDetail[] }>(`/devices/${id}/overview`);
      setDetail((res?.devices || [])[0] || null);
    } catch {
      /* keep stale detail */
    }
  }, []);

  const loadConfig = useCallback(async (id: string) => {
    if (!id) {
      setEditConfig(null);
      setConfigSnapshot("");
      return;
    }
    try {
      const res = await api<{ config?: DeviceConfig }>(`/devices/${id}/config`);
      const cfg = res?.config ? (JSON.parse(JSON.stringify(res.config)) as DeviceConfig) : null;
      if (cfg && isQmiControl(cfg.controlDevice)) cfg.deviceBackend = "qmi";
      setEditConfig(cfg);
      setConfigSnapshot(JSON.stringify(cfg));
    } catch {
      setEditConfig(null);
      setConfigSnapshot("");
    }
  }, []);

  const loadCardPolicy = useCallback(async (iccid?: string) => {
    const clean = (iccid || "").trim();
    if (!clean) {
      setCardPolicy(null);
      return;
    }
    try {
      setCardPolicy(await api<CardPolicy>(`/cards/${encodeURIComponent(clean)}/policy`));
    } catch {
      /* keep stale policy */
    }
  }, []);

  const loadDiscovered = useCallback(async () => {
    setDiscovering(true);
    // Never leave a previous physical scan visible while a new scan is in
    // progress or after it fails.
    setDiscovered([]);
    setAddSelected(null);
    setAddConfig(EMPTY_ADD);
    try {
      const res = await api<{ devices?: DiscoveredDevice[] }>("/devices/discovered?with_imei=1");
      const devices = Array.isArray(res?.devices) ? res!.devices! : [];
      setDiscovered(devices);
    } catch {
      setDiscovered([]);
    } finally {
      setDiscovering(false);
    }
  }, []);
  const selectDevice = useCallback(
    (id: string, updateUrl = true) => {
      const clean = (id || "").trim();
      if (!clean) return;
      setSelectedId(clean);
      if (updateUrl) {
        const p = new URLSearchParams(searchParamsRef.current);
        p.set("device", clean);
        p.set("tab", activeTabRef.current);
        setSearchParams(p, { replace: true });
      }
      void Promise.all([loadDetail(clean), loadConfig(clean)]);
    },
    [loadDetail, loadConfig, setSearchParams],
  );

  const loadDevices = useCallback(
    async (manual: boolean) => {
      if (manual) {
        setListLoading(true);
        setListError(null);
      }
      try {
        const res = await api<{ devices?: DeviceListItem[]; deviceLimit?: number }>("/devices");
        const devices = res?.devices || [];
        setList(devices);
        setDeviceLimit(typeof res?.deviceLimit === "number" ? res.deviceLimit : 0);
        setLastOkAt(Date.now());
        if (manual) setListError(null);
        const urlDevice = (searchParamsRef.current.get("device") || "").trim();
        let next = selectedIdRef.current;
        if (urlDevice) next = urlDevice;
        else if (!next && devices.length) next = devices[0].id;
        else if (next && devices.length === 0) next = "";
        if (next !== selectedIdRef.current) {
          setSelectedId(next);
          if (next) await Promise.all([loadDetail(next), loadConfig(next)]);
          else {
            setDetail(null);
            setEditConfig(null);
            setConfigSnapshot("");
          }
        } else if (next) {
          await loadDetail(next);
        }
      } catch (e) {
        if (manual) setListError({ message: apiMessage(e) || t("加载设备信息失败"), status: (e as { status?: number })?.status });
      } finally {
        if (manual) setListLoading(false);
      }
    },
    [loadDetail, loadConfig],
  );

  const refreshAll = useCallback(async () => {
    await Promise.all([loadDevices(false), selectedIdRef.current ? loadDetail(selectedIdRef.current) : Promise.resolve()]);
  }, [loadDevices, loadDetail]);

  const refreshSoon = useCallback(
    (ms: number) => {
      window.setTimeout(() => {
        refreshAll().catch(() => {});
      }, ms);
    },
    [refreshAll],
  );

  const handleProfileChanged = useCallback(() => {
    refreshAll().catch(() => {});
    refreshSoon(2000);
    refreshSoon(5000);
    refreshSoon(10000);
  }, [refreshAll, refreshSoon]);

  const handleTabChange = useCallback(
    (key: string) => {
      setActiveTab(key);
      const p = new URLSearchParams(searchParamsRef.current);
      if (selectedIdRef.current) p.set("device", selectedIdRef.current);
      p.set("tab", key);
      setSearchParams(p, { replace: true });
    },
    [setSearchParams],
  );
  const handleToggleRoamingData = useCallback(async (enabled: boolean) => {
    const id = selectedIdRef.current.trim();
    if (!id) return;
	setDataToggling(true);
    try {
	  await api(`/devices/${id}/network`, { method: "PATCH", body: { enabled } });
	  message.success(enabled ? t("漫游数据已开启，仅供 Export Proxy 使用") : t("漫游数据已关闭"));
      await refreshAll();
      refreshSoon(1500);
    } catch (e) {
	  message.error(apiMessage(e) || (enabled ? t("开启漫游数据失败") : t("关闭漫游数据失败")));
    } finally {
	  setDataToggling(false);
    }
  }, [refreshAll, refreshSoon]);

  const handleReconnectVoWiFi = useCallback(async () => {
    const id = selectedIdRef.current.trim();
    if (!id) return;
    const ok = await confirmDialog(tf("确定对设备 {id} 发起 VoWiFi 环境的重新连接拨号？这将在后台重新注册 IMS 链路。", { id }), t("重连 VoWiFi"), {
      confirmText: t("确定重连"),
      cancelText: t("取消"),
      type: "info",
    });
    if (!ok) return;
    setReconnectingVoWiFi(true);
    try {
      await api(`/devices/${id}/vowifi/actions/reconnect`, { method: "POST" });
      message.success(t("已触发重连指令，VoWiFi 服务正在重启..."));
      refreshAll().catch(() => {});
      refreshSoon(4000);
    } catch (e) {
      message.error(apiMessage(e) || t("重连命令下发失败"));
    } finally {
      setReconnectingVoWiFi(false);
    }
  }, [refreshAll, refreshSoon]);

  const handleRebootModem = useCallback(async () => {
    const id = selectedIdRef.current.trim();
    if (!id) return false;
    const ok = await confirmDialog(tf("确定对设备 {id} 发送重启模组指令？设备将在此期间脱网和失联数秒。", { id }), t("确认重启"), {
      confirmText: t("立即重启"),
      cancelText: t("取消"),
      type: "warning",
    });
    if (!ok) return false;
    setRebooting(true);
    try {
      await api(`/devices/${id}/actions/reboot`, { method: "POST" });
      message.success(t("重启指令已送达，设备正在重新启动"));
      refreshAll().catch(() => {});
      refreshSoon(5000);
      return true;
    } catch (e) {
      message.error(apiMessage(e) || t("指令下发失败"));
      return false;
    } finally {
      setRebooting(false);
    }
  }, [refreshAll, refreshSoon]);

  const handleRescan = useCallback(async () => {
    setRescanning(true);
    try {
      await api("/devices/actions/rescan", { method: "POST" });
      message.success(t("设备重新扫描完成"));
      await Promise.all([loadDevices(true), loadDiscovered()]);
    } catch (e) {
      message.error(apiMessage(e) || t("重新扫描失败"));
    } finally {
      setRescanning(false);
    }
  }, [loadDevices, loadDiscovered]);

  const handleOpenSms = useCallback(() => {
    const id = selectedIdRef.current;
    if (id) navigate(`/sms?device=${id}`);
  }, [navigate]);
  const handleSaveConfig = useCallback(async () => {
    const id = selectedIdRef.current.trim();
    if (!id || !editConfig) return;
    setSaving(true);
    try {
      const res = await api<{ requiresRestart?: boolean; warning?: string }>(`/devices/${id}`, {
        method: "PUT",
        body: { config: editConfig },
      });
      if (res?.warning) message.warning(res.warning);
      else if (res?.requiresRestart) message.warning(t("配置已保存，但部分变更需要重启服务后生效"));
      else message.success(t("配置已保存"));
      setConfigSnapshot(JSON.stringify(editConfig));
      await loadDevices(false);
      if (selectedIdRef.current) await loadDetail(selectedIdRef.current);
      await loadConfig(id);
    } catch (e) {
      message.error(apiMessage(e) || t("保存失败"));
    } finally {
      setSaving(false);
    }
  }, [editConfig, loadDevices, loadDetail, loadConfig]);

  const handleDeleteDevice = useCallback(async () => {
    const id = selectedIdRef.current.trim();
    if (!id) return;
    const ok = await confirmDialog(tf("确定删除设备 {id} 的配置？删除后该设备将停止接管（代理/网络/AT）。", { id }), t("确认删除"), {
      confirmText: t("删除"),
      cancelText: t("取消"),
      type: "warning",
    });
    if (!ok) return;
    setDeleting(true);
    try {
      await api(`/devices/${id}`, { method: "DELETE" });
      message.success(t("设备已删除"));
      setDetail(null);
      setEditConfig(null);
      setConfigSnapshot("");
      setSelectedId("");
      const p = new URLSearchParams(searchParamsRef.current);
      p.delete("device");
      setSearchParams(p, { replace: true });
      await loadDevices(true);
    } catch (e) {
      message.error(apiMessage(e) || t("删除失败"));
    } finally {
      setDeleting(false);
    }
  }, [loadDevices, setSearchParams]);

  const openAddDialog = useCallback(() => {
    setAddOpen(true);
    setAddSelected(null);
    setAddConfig(EMPTY_ADD);
    void loadDiscovered();
  }, [loadDiscovered]);

  const selectDiscovered = useCallback((d: DiscoveredDevice) => {
    if (d.discoveryIssue === "pcsc_service_unavailable") {
      message.warning(t("系统已发现 USB 读卡器，但 PC/SC 服务未运行；请安装并启动 pcscd 后重新扫描。"));
      return;
    }
    if (d.discoveryIssue === "pcsc_driver_missing") {
      message.warning(t("系统已发现 USB 读卡器，但 PC/SC 驱动未加载；请安装 libccid 或厂商驱动后重新扫描。"));
      return;
    }
    if (d.degraded) {
      message.warning(t("无法读取该设备 IMEI（可能控制口挂死），请执行 AT!RESET 或切换组态后重试"));
      return;
    }
    setAddSelected(d);
    setAddConfig((prev) => {
      const mode = String(d.mode || "").toLowerCase();
      const isReader = d.hardwareKind === "pcsc" || mode === "pcsc";
      const backend = isReader ? "pcsc" : mode === "mbim" ? "mbim" : isQmiControl(d.controlPath) || (mode === "qmi" && d.controlPath) ? "qmi" : "at";
      return {
        ...prev,
        interface: d.netInterface || "",
        atPort: d.atPort || "",
        controlDevice: d.controlPath || "",
        modemImei: d.imei || "",
        usbPath: d.usbPath || "",
        deviceBackend: backend,
		deviceType: d.deviceType || (isReader ? "usb_sim_reader" : prev.deviceType),
		esimTransport: isReader ? "pcsc" : backend,
      };
    });
  }, []);

  const saveAdd = useCallback(async () => {
    setAddSaving(true);
    try {
      if (!addSelected) {
        message.warning(t("请选择一个未配置设备"));
        return;
      }
	  if (!addConfig.deviceType) {
		message.warning(t("请选择设备类型"));
		return;
	  }
      const res = await api<{ warning?: string; started?: boolean }>("/devices", { method: "POST", body: { config: addConfig } });
      if (res?.warning) message.warning(res.warning);
      else if (res?.started === true) message.success(t("设备已添加并开始接管"));
      else message.success(t("设备配置已添加"));
      setAddOpen(false);
      await loadDevices(true);
    } catch (e) {
      message.error(apiMessage(e) || t("添加失败"));
    } finally {
      setAddSaving(false);
    }
  }, [addSelected, addConfig, loadDevices]);

  const setupE911 = useCallback(async () => {
    const id = selectedIdRef.current.trim();
    if (!id || e911Starting) return;
    setE911Starting(true);
    try {
      const data = await api<CarrierWebsheet>(`/devices/${id}/vowifi/e911/websheet`, { method: "POST" });
      setWebsheet(data);
      setWebsheetOpen(true);
    } catch (e) {
      message.error(apiMessage(e) || t("E911地址设置页面打开失败"));
    } finally {
      setE911Starting(false);
    }
  }, [e911Starting]);

  const handleWebsheetDone = useCallback(async () => {
    setWebsheetOpen(false);
    setWebsheet(null);
    await refreshAll();
  }, [refreshAll]);

  const handleCopyText = useCallback((text: string) => {
    const clean = (text || "").trim();
    if (!clean || clean === "--" || clean === "---") return;
    void copyText(clean, t("已复制"));
  }, []);

  const handlePolicyChanged = useCallback(async () => {
    await Promise.all([
      loadCardPolicy(detailRef.current?.modem?.iccid),
      selectedIdRef.current ? loadDetail(selectedIdRef.current) : Promise.resolve(),
    ]);
  }, [loadCardPolicy, loadDetail]);
  // URL -> state sync.
  useEffect(() => {
    const tab = (searchParams.get("tab") || "").trim();
    if (VALID_TABS.has(tab) && tab !== activeTabRef.current) setActiveTab(tab);
    const dev = (searchParams.get("device") || "").trim();
    if (dev && dev !== selectedIdRef.current) {
      setSelectedId(dev);
      void Promise.all([loadDetail(dev), loadConfig(dev)]);
    }
  }, [searchParams, loadDetail, loadConfig]);

  // Initial load + background polling.
  useEffect(() => {
    void loadDevices(true);
  }, [loadDevices]);
  usePolling(() => {
    void loadDevices(false);
  }, 15000, false);

  // The backend publishes a fresh overview snapshot every two seconds. Keep
  // the selected Overview tab live without reloading the page, and reconnect
  // quietly after transient network/server interruptions.
  useEffect(() => {
    if (activeTab !== "overview" || !selectedId) return;
    const streamDeviceID = selectedId;
    let stopped = false;
    let controller: AbortController | null = null;
    let retryTimer: number | null = null;

    const stopConnection = () => {
      controller?.abort();
      controller = null;
      if (retryTimer !== null) {
        window.clearTimeout(retryTimer);
        retryTimer = null;
      }
    };
    const scheduleReconnect = () => {
      if (stopped || document.hidden || retryTimer !== null) return;
      retryTimer = window.setTimeout(() => {
        retryTimer = null;
        connect();
      }, 2000);
    };
    const connect = () => {
      if (stopped || document.hidden || controller) return;
      const nextController = new AbortController();
      controller = nextController;
      readEventStream(
        `/devices/${encodeURIComponent(streamDeviceID)}/overview/stream`,
        {},
        {
          signal: nextController.signal,
          onEvent: (event, data) => {
            if (event !== "overview" || !data || selectedIdRef.current !== streamDeviceID) return;
            try {
              const nextDetail = camelize<DeviceDetail>(JSON.parse(data));
              setDetail(nextDetail);
              setList((current) =>
                current.map((item) => (item.id === streamDeviceID ? { ...item, ...nextDetail } : item)),
              );
            } catch {
              // Ignore one malformed event; the stream's next snapshot can
              // still update the page without forcing a reconnect.
            }
          },
        },
      )
        .catch(() => {})
        .finally(() => {
          if (controller === nextController) controller = null;
          if (!nextController.signal.aborted) scheduleReconnect();
        });
    };
    const onVisibilityChange = () => {
      if (document.hidden) stopConnection();
      else {
        void loadDetail(streamDeviceID);
        connect();
      }
    };

    connect();
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () => {
      stopped = true;
      stopConnection();
      document.removeEventListener("visibilitychange", onVisibilityChange);
    };
  }, [activeTab, selectedId, loadDetail]);

  // Card policy follows the current SIM.
  const iccid = detail?.modem?.iccid;
  useEffect(() => {
    void loadCardPolicy(iccid);
  }, [iccid, loadCardPolicy]);

  const filteredDevices = useMemo(() => {
    const q = query.trim().toLowerCase();
    let d = list.slice();
    if (statusFilter === "online") d = d.filter((c) => isDeviceOnline(c));
    else if (statusFilter === "offline") d = d.filter((c) => !c?.running && !isRecoveringPhase(c.lifecyclePhase));
    if (q) {
      d = d.filter((c) =>
        `${c?.id || ""} ${c?.name || ""} ${c?.modem?.iccid || ""} ${c?.modem?.imei || ""} ${c?.interface || ""}`
          .toLowerCase()
          .includes(q),
      );
    }
    d.sort((a, b) => {
      if (sortKey === "name") {
        const an = (a.name || a.id || "").toLowerCase();
        const bn = (b.name || b.id || "").toLowerCase();
        return an < bn ? (sortDir === "asc" ? -1 : 1) : an > bn ? (sortDir === "asc" ? 1 : -1) : 0;
      }
      if (sortKey === "signal") {
        const av = Number(a?.modem?.signalDbm ?? -999);
        const bv = Number(b?.modem?.signalDbm ?? -999);
        return sortDir === "asc" ? av - bv : bv - av;
      }
      return 0;
    });
    return d;
  }, [list, query, statusFilter, sortKey, sortDir]);

  const selectedListItem = useMemo(() => list.find((d) => d.id === selectedId) || null, [list, selectedId]);
  const simOperator = useMemo(() => simOperatorDisplay(detail), [detail]);
  const configDirty = editConfig && configSnapshot ? JSON.stringify(editConfig) !== configSnapshot : false;
  const unconfiguredDiscovered = useMemo(() => discovered.filter((d) => !d.configured), [discovered]);

  const detailOnline = isDeviceOnline(detail);
	const isReader = detail?.deviceType === "usb_sim_reader";
	const isNative410 = detail?.deviceType === "wifi_410";
	useEffect(() => {
		if (isReader && ["at", "ussd"].includes(activeTab)) setActiveTab("overview");
	}, [isReader, activeTab]);
  const addAtLimit = deviceLimit > 0 && list.length >= deviceLimit;
  const tabItems = [
    { key: "overview", label: t("概览") },
    { key: "esim", label: t("eSIM") },
    { key: "at", label: t("AT 终端") },
    { key: "ussd", label: t("USSD") },
    { key: "config", label: t("配置") },
    { key: "card", label: t("卡策略") },
  ].filter((tab) => !isReader || !["at", "ussd"].includes(tab.key));

  const overviewNode = detail ? (
    <div className="space-y-4">
      <DeviceOverviewTab
        device={detail}
        simOperatorDisplay={simOperator}
        customPhoneNumber={cardPolicy?.iccid === detail.modem?.iccid ? cardPolicy.customPhoneNumber : ""}
        trafficSpeedRx={''}
        trafficSpeedTx={''}
        trafficMinuteRx={''}
        trafficMinuteTx={''}
        e911Starting={e911Starting}
        onSetupE911={setupE911}
        onRefresh={refreshAll}
      />
    </div>
  ) : null;

  return (
    <div className="devices-page mx-auto w-full max-w-[1500px]">
      <PageHeader
        title={t("设备管理")}
        subtitle={t("接管 EC20 模组并执行射频、网络、SIM 与终端功能检测")}
        actions={
          <div className="flex items-center gap-2">
            <RefreshButton loading={listLoading} onClick={refreshAll} />
            <Button loading={rescanning} onClick={handleRescan} icon={<ArrowSyncRegular />}>
              {t("重新扫描")}
            </Button>
            <Button
              variant="primary"
              onClick={openAddDialog}
              disabled={addAtLimit}
              title={addAtLimit ? t("设备数量已达上限，无法继续添加") : undefined}
              className="!border-0"
              icon={<AddRegular />}
            >
              {t("添加设备")}
            </Button>
          </div>
        }
      />
      {listError ? (
        <ErrorState
          className="mb-4"
          title={t("设备列表加载失败")}
          message={listError.message}
          statusCode={listError.status}
          lastSuccessAt={lastOkAt}
          retryText={t("重试")}
          onRetry={() => void loadDevices(true)}
        />
      ) : null}
      <div className="devices-layout">
        {(!isMobile || !selectedId) && (
          <DeviceListPanel
            loading={listLoading}
            query={query}
            statusFilter={statusFilter}
            sortKey={sortKey}
            sortDir={sortDir}
            selectedId={selectedId}
            filteredDevices={filteredDevices}
            deviceCount={list.length}
            deviceLimit={deviceLimit}
            onQueryChange={setQuery}
            onStatusFilterChange={setStatusFilter}
            onSortKeyChange={setSortKey}
            onSortDirChange={setSortDir}
            onSelectDevice={(id) => selectDevice(id)}
          />
        )}
        <div className={`min-w-0 space-y-4 ${isMobile && selectedId ? "" : isMobile ? "hidden" : ""}`}>
          {isMobile && selectedId && detail ? (
            <Button variant="text" onClick={handleBackToList} icon={<ChevronLeftRegular />}>
              {t("返回")}
            </Button>
          ) : null}
          {detail ? (
            <>
              <DeviceDetailHeader
                device={detail}
				dataToggling={dataToggling}
                rebooting={rebooting}
                reconnectingVoWiFi={reconnectingVoWiFi}
                onCopyText={handleCopyText}
				onToggleRoamingData={handleToggleRoamingData}
                onReconnectVowifi={handleReconnectVoWiFi}
                onRebootModem={handleRebootModem}
                onOpenSms={handleOpenSms}
				wifiCallingOnly={isReader}
				modemControlOnly={isNative410}
              />
              <div className="device-detail-tabs ui-card p-6">
                <Tabs tabs={tabItems} value={activeTab} onChange={handleTabChange} />
                <div className="mt-5">
                  {activeTab === "overview" ? overviewNode : null}
                  {activeTab === "esim" ? (
                    <DeviceEsimTab
                      deviceId={detail.id}
                      deviceImei={detail.modem?.imei || ""}
                      isActive={activeTab === "esim"}
                      deviceOnline={detailOnline}
                      rebooting={rebooting}
                      onRebootModem={handleRebootModem}
                      onProfileChanged={handleProfileChanged}
                    />
                  ) : null}
                  {activeTab === "at" ? (
                    <DeviceAtTab deviceId={detail.id} backendMode={detail.backendMode} atPort={detail.atPort} running={detail.running} />
                  ) : null}
                  {activeTab === "ussd" ? <DeviceUssdTab deviceId={detail.id} /> : null}
                  {activeTab === "config" ? (
                    <DeviceConfigTab editConfig={editConfig} deviceStatus={detail} saving={saving} deleting={deleting} onSave={handleSaveConfig} onDelete={handleDeleteDevice} onEditConfig={setEditConfig} />
                  ) : null}
                  {activeTab === "card" ? (
                    <CardPolicyPanel deviceId={detail.id} iccid={detail.modem?.iccid} policy={cardPolicy} deviceOnline={detailOnline} onPolicyChanged={handlePolicyChanged} wifiCallingOnly={isReader} />
                  ) : null}
                </div>
              </div>
            </>
          ) : listLoading ? (
            <ListSkeleton rows={8} />
          ) : (
            <div className="ui-card p-10 text-center text-sm text-gray-400">
              {list.length === 0 ? t("暂无设备，点击右上角“添加设备”开始接管") : t("请选择左侧设备查看详情")}
            </div>
          )}
        </div>
      </div>
      <DeviceAddDialog
        open={addOpen}
        discovering={discovering}
        unconfiguredDiscovered={unconfiguredDiscovered}
        addSelected={addSelected}
        addConfig={addConfig}
        addSaving={addSaving}
        onClose={() => setAddOpen(false)}
        onRefresh={() => void loadDiscovered()}
        onSelectDevice={selectDiscovered}
        onConfigChange={setAddConfig}
        onSave={saveAdd}
      />
      <CarrierWebsheetDialog
        open={websheetOpen}
        websheet={websheet}
        onClose={() => setWebsheetOpen(false)}
        onDone={handleWebsheetDone}
      />
    </div>
  );
}
