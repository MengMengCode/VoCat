import { useCallback, useEffect, useMemo, useState } from "react";
import { AddRegular } from "@fluentui/react-icons";
import { api, ApiError, apiMessage } from "../api";
import type { DeviceListItem, DeviceProxyBinding, DevicesResponse, ProfileProxyCandidate, UpstreamProxy } from "../types";
import { usePolling } from "../lib/usePolling";
import { Button, PageHeader, confirmDialog, message } from "../components/ui";
import {
  emptyUpstreamForm,
  ipv6AddrError,
  type LoadError,
  type UpstreamForm,
  type UpstreamProbeResult,
  type UpstreamRow,
} from "../components/proxy/shared";
import { UpstreamDialog } from "../components/proxy/UpstreamDialog";
import { DeviceBindingsDialog } from "../components/proxy/DeviceBindingsDialog";
import { UpstreamSection } from "../components/proxy/UpstreamSection";
import { tf, useI18n } from "../lib/i18n";
import { listPlugins, pluginAssetURL, type InstalledPlugin } from "../extensions";

interface BindingMutationResult {
  reconnectRequested?: boolean;
  reconnectError?: string;
}

export default function ProxyPage() {
  const { t } = useI18n();

  const [proxies, setProxies] = useState<UpstreamProxy[]>([]);
  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [bindings, setBindings] = useState<DeviceProxyBinding[]>([]);
  const [upstreamLoading, setUpstreamLoading] = useState(true);
  const [upstreamError, setUpstreamError] = useState<LoadError | null>(null);
  const [upstreamDialogOpen, setUpstreamDialogOpen] = useState(false);
  const [editingUpstream, setEditingUpstream] = useState<UpstreamProxy | null>(null);
  const [upstreamForm, setUpstreamForm] = useState<UpstreamForm>(emptyUpstreamForm());
  const [testingUpstream, setTestingUpstream] = useState(false);
  const [upstreamProbe, setUpstreamProbe] = useState<UpstreamProbeResult | null>(null);
  const [bindingsDialogOpen, setBindingsDialogOpen] = useState(false);
  const [bindingsProxy, setBindingsProxy] = useState<UpstreamProxy | null>(null);
  const [bindingBusy, setBindingBusy] = useState(false);
  const [plugins, setPlugins] = useState<InstalledPlugin[]>([]);

  const proxyRows = useMemo<UpstreamRow[]>(
    () => proxies.map((proxy) => ({
      ...proxy,
      bindingCount: bindings.filter((binding) => binding.upstreamProxyId === proxy.id).length,
    })),
    [proxies, bindings],
  );

  const loadUpstream = useCallback(async (initial = false) => {
    if (initial) setUpstreamLoading(true);
    setUpstreamError(null);
    try {
      const [proxyList, bindingList, deviceList] = await Promise.all([
        api<UpstreamProxy[]>("/upstream-proxies"),
        api<DeviceProxyBinding[]>("/upstream-proxy-profile-bindings"),
        api<DevicesResponse>("/devices"),
      ]);
      setProxies(proxyList || []);
      setBindings(bindingList || []);
      setDevices(deviceList?.devices || []);
    } catch (error) {
      setUpstreamError({ message: apiMessage(error), status: error instanceof ApiError ? error.status : undefined });
    } finally {
      if (initial) setUpstreamLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadUpstream(true);
  }, [loadUpstream]);

  useEffect(() => {
    let active = true;
    const load = () => listPlugins().then((items) => {
      if (active) setPlugins(items || []);
    }).catch(() => undefined);
    void load();
    window.addEventListener("vocat:plugins-changed", load);
    return () => { active = false; window.removeEventListener("vocat:plugins-changed", load); };
  }, []);

  usePolling(() => {
    if (!upstreamLoading) void loadUpstream(false);
  }, 10000, false);

  const openUpstreamDialog = useCallback((proxy?: UpstreamProxy) => {
    setUpstreamProbe(null);
    if (proxy) {
      setEditingUpstream(proxy);
      setUpstreamForm({
        id: proxy.id,
        name: proxy.name,
        addr: proxy.addr,
        username: proxy.username,
        password: proxy.password === "****" ? "" : proxy.password ?? "",
        enabled: proxy.enabled,
      });
    } else {
      setEditingUpstream(null);
      setUpstreamForm(emptyUpstreamForm());
    }
    setUpstreamDialogOpen(true);
  }, []);

  const submitUpstream = useCallback(async () => {
    const form = { ...upstreamForm };
    form.id = (form.id || "").trim();
    form.name = (form.name || "").trim();
    form.addr = (form.addr || "").trim();
    if (!form.id) return message.warning(t("ID 不能为空"));
    if (!form.addr) return message.warning(t("SOCKS5 地址不能为空"));
    const ipv6Error = ipv6AddrError(form.addr);
    if (ipv6Error) return message.warning(ipv6Error);
    try {
      if (editingUpstream) {
        await api(`/upstream-proxies/${form.id}`, { method: "PUT", body: form });
        message.success(t("上游代理已更新，并已执行连通性探测"));
      } else {
        await api("/upstream-proxies", { method: "POST", body: form });
        message.success(t("上游代理已创建，并已执行连通性探测"));
      }
      setUpstreamDialogOpen(false);
      await loadUpstream(false);
    } catch (error) {
      message.error(apiMessage(error) || t("保存失败"));
    }
  }, [upstreamForm, editingUpstream, loadUpstream, t]);

  const testUpstream = useCallback(async () => {
    const form = { ...upstreamForm, addr: (upstreamForm.addr || "").trim() };
    if (!form.addr) return message.warning(t("请先填写 SOCKS5 地址"));
    const ipv6Error = ipv6AddrError(form.addr);
    if (ipv6Error) return message.warning(ipv6Error);
    setTestingUpstream(true);
    setUpstreamProbe(null);
    try {
      const data = await api<{ status?: string; probe?: UpstreamProbeResult; message?: string }>("/upstream-proxy-probe", {
        method: "POST",
        body: {
          id: editingUpstream?.id || "",
          addr: form.addr,
          username: (form.username || "").trim(),
          password: form.password || "",
        },
      });
      setUpstreamProbe(data.probe || null);
      if (data.probe?.udpAssociateOk) {
        message.success(data.message || t("SOCKS5 鉴权和 UDP Associate 探测通过"));
      } else {
        message.warning(data.message || t("代理不能承载 VoWiFi 所需的 UDP"));
      }
    } catch (error) {
      message.error(apiMessage(error) || t("连通性检测失败"));
    } finally {
      setTestingUpstream(false);
    }
  }, [upstreamForm, editingUpstream, t]);

  const removeUpstream = useCallback(async (proxy: UpstreamProxy) => {
    const ok = await confirmDialog(
      <>
        {tf("确定删除上游代理“{name}”？", { name: proxy.name || proxy.id })}
        <br />
        {t("绑定到该代理的 Profile 将自动解绑并恢复直连。")}
      </>,
      t("确认删除"),
      { confirmText: t("删除"), cancelText: t("取消"), type: "warning" },
    );
    if (!ok) return;
    try {
      await api(`/upstream-proxies/${proxy.id}`, { method: "DELETE" });
      message.success(t("上游代理已删除"));
      if (bindingsProxy?.id === proxy.id) setBindingsDialogOpen(false);
      await loadUpstream(false);
    } catch (error) {
      message.error(apiMessage(error) || t("删除失败"));
    }
  }, [bindingsProxy, loadUpstream, t]);

  const openBindingsDialog = useCallback((proxy: UpstreamProxy) => {
    setBindingsProxy(proxy);
    setBindingsDialogOpen(true);
  }, []);

  const showRouteChangeResult = useCallback((result: BindingMutationResult, successText: string) => {
    if (result.reconnectError) {
      message.warning(`${successText}；${t("线路已保存，将在下次启动 VoWiFi 时应用")}`);
    } else if (result.reconnectRequested) {
      message.success(`${successText}；${t("VoWiFi 线路刷新已安排")}`);
    } else {
      message.success(`${successText}；${t("将在下次开启 VoWiFi 时生效")}`);
    }
  }, [t]);

  const addProfileBindings = useCallback(async (profiles: ProfileProxyCandidate[]) => {
    if (!bindingsProxy || profiles.length === 0) return;
    setBindingBusy(true);
    try {
      const result = await api<BindingMutationResult>("/upstream-proxy-profile-bindings", {
        method: "POST",
        body: {
          upstreamProxyId: bindingsProxy.id,
          bindings: profiles.map(({ deviceId, iccid, profileName }) => ({ deviceId, iccid, profileName })),
        },
      });
      showRouteChangeResult(result, t("Profile 已绑定"));
      await loadUpstream(false);
    } catch (error) {
      const code = error instanceof ApiError ? error.code : "";
      if (code === "profile_already_bound") {
        message.error(t("所选 ICCID 已绑定其他代理，请先删除原绑定"));
      } else {
        message.error(apiMessage(error) || t("绑定失败"));
      }
    } finally {
      setBindingBusy(false);
    }
  }, [bindingsProxy, loadUpstream, showRouteChangeResult, t]);

  const deleteProfileBindings = useCallback(async (iccids: string[]) => {
    if (!bindingsProxy || iccids.length === 0) return;
    setBindingBusy(true);
    try {
      const result = await api<BindingMutationResult>("/upstream-proxy-profile-bindings", {
        method: "DELETE",
        body: { upstreamProxyId: bindingsProxy.id, iccids },
      });
      showRouteChangeResult(result, t("所选 Profile 绑定已删除"));
      await loadUpstream(false);
    } catch (error) {
      message.error(apiMessage(error) || t("删除绑定失败"));
    } finally {
      setBindingBusy(false);
    }
  }, [bindingsProxy, loadUpstream, showRouteChangeResult, t]);

  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        title={t("代理管理")}
        subtitle={t("管理 VoWiFi 上游代理和 eSIM Profile 绑定")}
        actions={<Button variant="primary" icon={<AddRegular />} onClick={() => openUpstreamDialog()}>{t("新增代理")}</Button>}
      />
      <UpstreamSection
        rows={proxyRows}
        loading={upstreamLoading}
        error={upstreamError}
        onRetry={() => loadUpstream(false)}
        onEdit={openUpstreamDialog}
        onDelete={removeUpstream}
        onOpenBindings={openBindingsDialog}
      />
      {plugins.filter((plugin) => plugin.enabled).flatMap((plugin) =>
        plugin.contributions.filter((contribution) => contribution.location === "proxy").map((contribution) => (
          <section key={`${plugin.id}:${contribution.id}`} className="ui-card mt-6 overflow-hidden p-0">
            <iframe
              title={contribution.label}
              src={pluginAssetURL(plugin, contribution)}
              className="h-[640px] w-full border-0 bg-white dark:bg-[#15151a]"
              sandbox="allow-scripts allow-forms allow-same-origin"
            />
          </section>
        )),
      )}
      <UpstreamDialog
        open={upstreamDialogOpen}
        editing={!!editingUpstream}
        form={upstreamForm}
        testing={testingUpstream}
        probe={upstreamProbe}
        onPatch={(patch) => {
          if ("addr" in patch || "username" in patch || "password" in patch) setUpstreamProbe(null);
          setUpstreamForm((form) => ({ ...form, ...patch }));
        }}
        onTest={() => void testUpstream()}
        onClose={() => {
          setUpstreamDialogOpen(false);
          setUpstreamProbe(null);
        }}
        onSubmit={submitUpstream}
      />
      <DeviceBindingsDialog
        open={bindingsDialogOpen}
        proxy={bindingsProxy}
        proxies={proxies}
        devices={devices}
        bindings={bindings}
        busy={bindingBusy}
        onAdd={(profiles) => void addProfileBindings(profiles)}
        onDelete={(iccids) => void deleteProfileBindings(iccids)}
        onClose={() => setBindingsDialogOpen(false)}
      />
    </div>
  );
}
