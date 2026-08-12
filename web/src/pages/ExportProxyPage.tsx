import { useCallback, useEffect, useMemo, useState } from "react";
import { AddRegular, DeleteRegular, EditRegular, GlobeRegular } from "@fluentui/react-icons";
import { api, apiMessage } from "../api";
import type { DeviceListItem, DevicesResponse } from "../types";
import { Button, Input, Modal, PageHeader, Select, Switch, Tag, confirmDialog, message } from "../components/ui";
import { useI18n } from "../lib/i18n";

interface ExportProxyConfig {
  id: string;
  name: string;
  deviceId: string;
  interface: string;
  mode: "http" | "socks5";
  listenHost: string;
  listenPort: number;
  enabled: boolean;
  authEnabled: boolean;
  username: string;
  password: string;
}

interface ExportProxyStatus {
  id: string;
  running: boolean;
  listen?: string;
  error?: string;
}

const emptyConfig = (): ExportProxyConfig => ({
  id: "",
  name: "",
  deviceId: "",
  interface: "",
  mode: "socks5",
  listenHost: "0.0.0.0",
  listenPort: 1080,
  enabled: true,
  authEnabled: false,
  username: "",
  password: "",
});

export default function ExportProxyPage() {
  const { t } = useI18n();
  const [configs, setConfigs] = useState<ExportProxyConfig[]>([]);
  const [statuses, setStatuses] = useState<ExportProxyStatus[]>([]);
  const [devices, setDevices] = useState<DeviceListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<ExportProxyConfig>(emptyConfig);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState("");

  const load = useCallback(async (initial = false) => {
    if (initial) setLoading(true);
    try {
      const [configData, statusData, deviceData] = await Promise.all([
        api<{ configs?: ExportProxyConfig[] }>("/export-proxies"),
        api<{ configs?: ExportProxyStatus[] }>("/export-proxies/status"),
        api<DevicesResponse>("/devices"),
      ]);
      setConfigs(configData.configs || []);
      setStatuses(statusData.configs || []);
      setDevices((deviceData.devices || []).filter((device) => !!device.interface));
      setError("");
    } catch (err) {
      setError(apiMessage(err));
    } finally {
      if (initial) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(true);
    const timer = window.setInterval(() => void load(), 5000);
    return () => window.clearInterval(timer);
  }, [load]);

  const statusByID = useMemo(() => new Map(statuses.map((status) => [status.id, status])), [statuses]);
  const deviceByID = useMemo(() => new Map(devices.map((device) => [device.id, device])), [devices]);

  const edit = (config?: ExportProxyConfig) => {
    if (config) {
      setForm({ ...config, password: "" });
    } else {
      const first = devices[0];
      setForm({ ...emptyConfig(), deviceId: first?.id || "", interface: first?.interface || "" });
    }
    setOpen(true);
  };

  const chooseDevice = (deviceId: string) => {
    const device = deviceByID.get(deviceId);
    setForm((current) => ({ ...current, deviceId, interface: device?.interface || "" }));
  };

  const save = async () => {
    if (!form.deviceId) return message.warning(t("请选择设备"));
    if (!form.listenPort || form.listenPort < 1 || form.listenPort > 65535) return message.warning(t("请输入有效端口"));
    if (form.authEnabled && !form.username.trim()) return message.warning(t("启用认证后必须填写用户名"));
    setSaving(true);
    try {
      if (form.id) {
        await api(`/export-proxies/${encodeURIComponent(form.id)}`, { method: "PUT", body: form });
        message.success(t("导出代理已更新"));
      } else {
        await api("/export-proxies", { method: "POST", body: form });
        message.success(t("导出代理已创建"));
      }
      setOpen(false);
      await load();
    } catch (err) {
      message.error(apiMessage(err) || t("保存失败"));
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (config: ExportProxyConfig) => {
    setBusy(config.id);
    try {
      await api(`/export-proxies/${encodeURIComponent(config.id)}`, {
        method: "PUT",
        body: { ...config, enabled: !config.enabled },
      });
      await load();
    } catch (err) {
      message.error(apiMessage(err));
    } finally {
      setBusy("");
    }
  };

  const remove = async (config: ExportProxyConfig) => {
    if (!await confirmDialog(t("确定删除这个导出代理配置吗？"), t("确认删除"), { type: "warning", confirmText: t("删除"), cancelText: t("取消") })) return;
    setBusy(config.id);
    try {
      await api(`/export-proxies/${encodeURIComponent(config.id)}`, { method: "DELETE" });
      message.success(t("导出代理已删除"));
      await load();
    } catch (err) {
      message.error(apiMessage(err));
    } finally {
      setBusy("");
    }
  };

  return (
    <div className="mx-auto max-w-7xl">
      <PageHeader
        title={t("导出代理")}
        subtitle={t("将模块漫游数据导出为主机 HTTP 或 SOCKS5 代理")}
        actions={<Button variant="primary" icon={<AddRegular />} onClick={() => edit()} disabled={!devices.length}>{t("添加代理")}</Button>}
      />

      <div className="ui-card overflow-hidden">
        {error ? <div className="border-b border-red-200 bg-red-50 p-3 text-sm text-red-600 dark:border-red-500/20 dark:bg-red-500/10 dark:text-red-300">{error}</div> : null}
        <div className="overflow-x-auto">
          <table className="w-full min-w-[900px] text-left text-sm">
            <thead className="border-b border-gray-100 bg-gray-50/70 text-xs uppercase tracking-wide text-gray-500 dark:border-white/10 dark:bg-white/[0.025]">
              <tr>
                <th className="px-4 py-3">{t("名称")}</th>
                <th className="px-4 py-3">{t("设备")}</th>
                <th className="px-4 py-3">{t("网络接口")}</th>
                <th className="px-4 py-3">{t("协议")}</th>
                <th className="px-4 py-3">{t("监听地址")}</th>
                <th className="px-4 py-3">{t("认证")}</th>
                <th className="px-4 py-3">{t("状态")}</th>
                <th className="px-4 py-3 text-right">{t("操作")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-white/10">
              {configs.map((config) => {
                const status = statusByID.get(config.id);
                const device = deviceByID.get(config.deviceId);
                return (
                  <tr key={config.id} className="hover:bg-sky-50/40 dark:hover:bg-sky-500/[0.04]">
                    <td className="px-4 py-3 font-semibold">{config.name}</td>
                    <td className="px-4 py-3">{device?.name || config.deviceId}</td>
                    <td className="px-4 py-3 font-mono text-xs">{config.interface}</td>
                    <td className="px-4 py-3"><Tag type="primary">{config.mode.toUpperCase()}</Tag></td>
                    <td className="px-4 py-3 font-mono text-xs">{status?.listen || `${config.listenHost}:${config.listenPort}`}</td>
                    <td className="px-4 py-3">{config.authEnabled ? config.username : t("无")}</td>
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <Switch checked={config.enabled} loading={busy === config.id} onChange={() => void toggle(config)} size="small" />
                        <span className={status?.running ? "text-green-600" : status?.error ? "text-red-500" : "text-gray-400"}>
                          {status?.running ? t("运行中") : status?.error ? t("错误") : t("已停用")}
                        </span>
                      </div>
                      {status?.error ? <div className="mt-1 max-w-xs text-xs text-red-500">{status.error}</div> : null}
                    </td>
                    <td className="px-4 py-3">
                      <div className="flex justify-end gap-2">
                        <Button size="small" icon={<EditRegular />} onClick={() => edit(config)}>{t("编辑")}</Button>
                        <Button size="small" variant="danger" plain icon={<DeleteRegular />} loading={busy === config.id} onClick={() => void remove(config)}>{t("删除")}</Button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        {!loading && !configs.length ? (
          <div className="flex flex-col items-center justify-center px-6 py-16 text-center text-gray-400">
            <GlobeRegular className="mb-3 text-4xl" />
            <div className="text-sm">{t("暂无导出代理配置")}</div>
            <div className="mt-1 text-xs">{t("先在设备页面开启漫游数据，再创建代理")}</div>
          </div>
        ) : null}
        {loading ? <div className="px-6 py-16 text-center text-sm text-gray-400">{t("加载中...")}</div> : null}
      </div>

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title={form.id ? t("编辑导出代理") : t("添加导出代理")}
        width="max-w-2xl"
        footer={<><Button onClick={() => setOpen(false)}>{t("取消")}</Button><Button variant="primary" loading={saving} onClick={() => void save()}>{t("保存")}</Button></>}
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <label className="space-y-1.5 text-sm"><span>{t("名称")}</span><Input value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder={t("例如：EC20 漫游出口")} /></label>
          <label className="space-y-1.5 text-sm"><span>{t("设备")}</span><Select value={form.deviceId} onChange={chooseDevice} options={devices.map((device) => ({ value: device.id, label: `${device.name || device.id} · ${device.interface}` }))} /></label>
          <label className="space-y-1.5 text-sm"><span>{t("网络接口")}</span><Input value={form.interface} readOnly disabled /></label>
          <label className="space-y-1.5 text-sm"><span>{t("协议")}</span><Select value={form.mode} onChange={(value) => setForm({ ...form, mode: value as "http" | "socks5" })} options={[{ value: "socks5", label: "SOCKS5" }, { value: "http", label: "HTTP" }]} /></label>
          <label className="space-y-1.5 text-sm"><span>{t("监听地址")}</span><Input value={form.listenHost} onChange={(event) => setForm({ ...form, listenHost: event.target.value })} /></label>
          <label className="space-y-1.5 text-sm"><span>{t("端口")}</span><Input type="number" min={1} max={65535} value={form.listenPort} onChange={(event) => setForm({ ...form, listenPort: Number(event.target.value) })} /></label>
          <div className="flex items-center justify-between rounded-lg border border-gray-200 px-3 py-2 dark:border-white/10"><span className="text-sm">{t("代理认证")}</span><Switch checked={form.authEnabled} onChange={(authEnabled) => setForm({ ...form, authEnabled })} /></div>
          <div className="flex items-center justify-between rounded-lg border border-gray-200 px-3 py-2 dark:border-white/10"><span className="text-sm">{t("保存后立即启用")}</span><Switch checked={form.enabled} onChange={(enabled) => setForm({ ...form, enabled })} /></div>
          {form.authEnabled ? <>
            <label className="space-y-1.5 text-sm"><span>{t("用户名")}</span><Input value={form.username} onChange={(event) => setForm({ ...form, username: event.target.value })} autoComplete="off" /></label>
            <label className="space-y-1.5 text-sm"><span>{t("密码")}</span><Input type="password" value={form.password} onChange={(event) => setForm({ ...form, password: event.target.value })} autoComplete="new-password" placeholder={form.id ? t("留空则保留原密码") : ""} /></label>
          </> : null}
        </div>
      </Modal>
    </div>
  );
}
