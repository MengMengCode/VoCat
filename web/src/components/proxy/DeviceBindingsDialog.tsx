import { AddRegular, DeleteRegular } from "@fluentui/react-icons";
import { useEffect, useMemo, useState } from "react";
import { api, apiMessage } from "../../api";
import type { DeviceListItem, DeviceProxyBinding, EsimOverview, ProfileProxyCandidate, UpstreamProxy } from "../../types";
import { Button, EmptyState, Modal, Tag, message } from "../ui";
import { useI18n } from "../../lib/i18n";

export interface DeviceBindingsDialogProps {
  open: boolean;
  proxy: UpstreamProxy | null;
  proxies: UpstreamProxy[];
  devices: DeviceListItem[];
  bindings: DeviceProxyBinding[];
  busy: boolean;
  onAdd: (profiles: ProfileProxyCandidate[]) => void;
  onDelete: (iccids: string[]) => void;
  onClose: () => void;
}

function profileLabel(profile: { name?: string; serviceProviderName?: string; iccid: string }) {
  return String(profile.name || profile.serviceProviderName || profile.iccid).trim();
}

export function DeviceBindingsDialog(props: DeviceBindingsDialogProps) {
  const { t } = useI18n();
  const { open, proxy, proxies, devices, bindings, busy, onAdd, onDelete, onClose } = props;
  const [adding, setAdding] = useState(false);
  const [loadingProfiles, setLoadingProfiles] = useState(false);
  const [candidates, setCandidates] = useState<ProfileProxyCandidate[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const proxyName = proxy?.name || proxy?.id || "";
  const deviceKey = devices.map((device) => device.id).sort().join("|");
  const current = useMemo(
    () => bindings.filter((item) => item.upstreamProxyId === proxy?.id),
    [bindings, proxy?.id],
  );
  const bindingByICCID = useMemo(() => new Map(bindings.map((item) => [item.iccid, item])), [bindings]);
  const proxyNameById = useMemo(() => new Map(proxies.map((item) => [item.id, item.name || item.id])), [proxies]);

  useEffect(() => {
    if (!open) {
      setAdding(false);
      setSelected([]);
      setCandidates([]);
    }
  }, [open]);

  useEffect(() => {
    if (!adding || !open) return;
    let active = true;
    setLoadingProfiles(true);
    Promise.allSettled(devices.map(async (device) => {
      const data = await api<EsimOverview>(`/devices/${encodeURIComponent(device.id)}/esim`);
      return (data.profiles || []).flatMap((group) => (group.profiles || []).map((profile) => ({
        deviceId: device.id,
        iccid: String(profile.iccid || "").trim(),
        profileName: profileLabel(profile),
        stateText: profile.stateText,
      }))).filter((profile) => profile.iccid);
    })).then((results) => {
      if (!active) return;
      const unique = new Map<string, ProfileProxyCandidate>();
      for (const result of results) {
        if (result.status !== "fulfilled") continue;
        for (const profile of result.value) if (!unique.has(profile.iccid)) unique.set(profile.iccid, profile);
      }
      setCandidates(Array.from(unique.values()).sort((a, b) => a.deviceId.localeCompare(b.deviceId) || a.profileName.localeCompare(b.profileName)));
    }).catch((error) => {
      if (active) message.error(apiMessage(error) || t("读取 eSIM Profile 失败"));
    }).finally(() => {
      if (active) setLoadingProfiles(false);
    });
    return () => { active = false; };
  }, [adding, open, deviceKey, t]);

  useEffect(() => {
    if (!adding || selected.length === 0) return;
    if (selected.every((iccid) => bindingByICCID.get(iccid)?.upstreamProxyId === proxy?.id)) {
      setAdding(false);
      setSelected([]);
    }
  }, [adding, selected, bindingByICCID, proxy?.id]);

  useEffect(() => {
    if (adding) return;
    const available = new Set(current.map((item) => item.iccid));
    setSelected((items) => items.filter((iccid) => available.has(iccid)));
  }, [adding, current]);

  const rows = adding ? candidates : current;
  const selectable = rows.filter((row) => adding ? !bindingByICCID.has(row.iccid) : true).map((row) => row.iccid);
  const allSelected = selectable.length > 0 && selectable.every((iccid) => selected.includes(iccid));
  const toggle = (iccid: string) => setSelected((values) => values.includes(iccid) ? values.filter((item) => item !== iccid) : [...values, iccid]);
  const toggleAll = () => setSelected(allSelected ? [] : selectable);

  return (
    <Modal open={open} onClose={onClose} title={`${adding ? t("添加 Profile 绑定") : t("Profile 绑定")} — ${proxyName}`} width="max-w-5xl">
      <div className="space-y-4 pb-2">
        <div className="rounded-lg border border-sky-200/70 bg-sky-50 px-3 py-2 text-xs text-sky-800 dark:border-sky-800/50 dark:bg-sky-900/20 dark:text-sky-200">
          {t("VoWiFi 会按当前 ICCID 选择代理。同一 ICCID 只能绑定一个代理，一个代理可以绑定多台设备上的多个 Profile。")}
        </div>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="text-xs text-gray-500">{adding ? t("从设备已安装的 eSIM Profile 中选择") : `${current.length} ${t("个 Profile")}`}</div>
          <div className="flex gap-2">
            {adding ? (
              <Button size="small" onClick={() => { setAdding(false); setSelected([]); }}>{t("返回绑定列表")}</Button>
            ) : null}
            {adding ? (
              <Button
                size="small"
                variant="primary"
                icon={<AddRegular />}
                loading={busy}
                disabled={selected.length === 0 || loadingProfiles}
                onClick={() => onAdd(candidates.filter((item) => selected.includes(item.iccid)))}
              >{t("添加所选")}</Button>
            ) : (
              <>
                <Button size="small" variant="danger" plain icon={<DeleteRegular />} loading={busy} disabled={selected.length === 0} onClick={() => onDelete(selected)}>{t("删除所选")}</Button>
                <Button size="small" variant="primary" icon={<AddRegular />} onClick={() => { setAdding(true); setSelected([]); }}>{t("添加")}</Button>
              </>
            )}
          </div>
        </div>

        <div className="overflow-x-auto rounded-xl border border-gray-100 dark:border-white/10">
          <table className="w-full min-w-[760px] text-left text-sm">
            <thead className="bg-gray-50/80 text-xs uppercase tracking-wide text-gray-500 dark:bg-white/[0.025]">
              <tr>
                <th className="w-12 px-4 py-3"><input type="checkbox" checked={allSelected} onChange={toggleAll} disabled={selectable.length === 0 || busy} aria-label={t("全选")} /></th>
                <th className="px-4 py-3">{t("设备 ID")}</th>
                <th className="px-4 py-3">ICCID</th>
                <th className="px-4 py-3">{t("Profile 名称")}</th>
                {adding ? <th className="px-4 py-3">{t("状态")}</th> : null}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-white/10">
              {rows.map((row) => {
                const existing = bindingByICCID.get(row.iccid);
                const unavailable = adding && !!existing;
                return (
                  <tr key={`${row.deviceId}:${row.iccid}`} className={unavailable ? "opacity-60" : "hover:bg-sky-50/40 dark:hover:bg-sky-500/[0.04]"}>
                    <td className="px-4 py-3"><input type="checkbox" checked={selected.includes(row.iccid)} onChange={() => toggle(row.iccid)} disabled={unavailable || busy} aria-label={row.iccid} /></td>
                    <td className="px-4 py-3 font-mono text-xs">{row.deviceId}</td>
                    <td className="px-4 py-3 font-mono text-xs">{row.iccid}</td>
                    <td className="px-4 py-3 font-medium">{row.profileName || row.iccid}</td>
                    {adding ? (
                      <td className="px-4 py-3">
                        {existing ? <Tag type={existing.upstreamProxyId === proxy?.id ? "success" : "info"}>{existing.upstreamProxyId === proxy?.id ? t("已绑定此代理") : `${t("已绑定")}: ${proxyNameById.get(existing.upstreamProxyId) || existing.upstreamProxyId}`}</Tag> : <Tag type="primary">{("stateText" in row && row.stateText) || t("可绑定")}</Tag>}
                      </td>
                    ) : null}
                  </tr>
                );
              })}
            </tbody>
          </table>
          {loadingProfiles ? <div className="px-6 py-12 text-center text-sm text-gray-400">{t("读取 Profile 中...")}</div> : null}
          {!loadingProfiles && rows.length === 0 ? <EmptyState title={adding ? t("没有可显示的 eSIM Profile") : t("尚未绑定 Profile")} subtitle={adding ? t("请确认设备在线且支持 eSIM Profile 列表读取。") : t("点击添加，从设备 Profile 列表中选择。")}/>: null}
        </div>
      </div>
    </Modal>
  );
}
