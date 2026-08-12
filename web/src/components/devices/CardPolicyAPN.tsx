import { useCallback, useEffect, useMemo, useState } from "react";
import { apiMessage } from "../../api";
import type { CardPolicy } from "../../types";
import { Button, Input, Modal, Select, Spinner, Switch, Tag, confirmDialog, message } from "../ui";
import { useI18n } from "../../lib/i18n";
import {
  createCardAPN,
  deleteCardAPN,
  getCardAPNs,
  getDeviceAPNs,
  updateCardAPN,
  updateCardPolicy,
  type CardAPNProfile,
  type ModemAPNProfile,
} from "./deviceActions";

interface CardPolicyAPNProps {
  deviceId: string;
  iccid: string;
  policy: CardPolicy | null;
  deviceOnline: boolean;
  onSaved: (policy: CardPolicy) => void;
}

interface APNRow {
  key: string;
  apn: string;
  ipVersion: "IP" | "IPV6" | "IPV4V6";
  source: "automatic" | "modem" | "custom";
  cid?: number;
  customID?: number;
  username?: string;
  hasPassword?: boolean;
  proxy?: string;
  mcc?: string;
  mnc?: string;
  roamingIPVersion?: "IP" | "IPV6" | "IPV4V6";
  authType?: "NONE" | "PAP" | "CHAP" | "PAP_OR_CHAP";
}

const APN_PATTERN = /^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9])?$/;

export function CardPolicyAPN({ deviceId, iccid, policy, deviceOnline, onSaved }: CardPolicyAPNProps) {
  const { t } = useI18n();
  const [modemProfiles, setModemProfiles] = useState<ModemAPNProfile[]>([]);
  const [customProfiles, setCustomProfiles] = useState<CardAPNProfile[]>([]);
  const [loading, setLoading] = useState(true);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingProfile, setEditingProfile] = useState<CardAPNProfile | null>(null);
  const [newAPN, setNewAPN] = useState("");
  const [newUsername, setNewUsername] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [newProxy, setNewProxy] = useState("");
  const [newMCC, setNewMCC] = useState("");
  const [newMNC, setNewMNC] = useState("");
  const [newIPVersion, setNewIPVersion] = useState<"IP" | "IPV6" | "IPV4V6">("IPV4V6");
  const [newRoamingIPVersion, setNewRoamingIPVersion] = useState<"IP" | "IPV6" | "IPV4V6">("IP");
  const [newAuthType, setNewAuthType] = useState<"NONE" | "PAP" | "CHAP" | "PAP_OR_CHAP">("NONE");
  const [clearPassword, setClearPassword] = useState(false);
  const [adding, setAdding] = useState(false);
  const [pendingKey, setPendingKey] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    const [customResult, modemResult] = await Promise.allSettled([
      getCardAPNs(iccid),
      deviceOnline ? getDeviceAPNs(deviceId) : Promise.resolve({ items: [] as ModemAPNProfile[] }),
    ]);
    setCustomProfiles(customResult.status === "fulfilled" ? customResult.value.items || [] : []);
    setModemProfiles(modemResult.status === "fulfilled" ? modemResult.value.items || [] : []);
    setLoading(false);
  }, [deviceId, deviceOnline, iccid]);

  useEffect(() => {
    load();
  }, [load]);

  const rows = useMemo<APNRow[]>(() => {
    const result: APNRow[] = [
      { key: "automatic", apn: "", ipVersion: "IPV4V6", source: "automatic" },
    ];
    for (const item of modemProfiles) {
      result.push({
        key: `modem:${item.cid}:${item.apn}:${item.ipVersion}`,
        apn: item.apn,
        ipVersion: item.ipVersion,
        source: "modem",
        cid: item.cid,
      });
    }
    for (const item of customProfiles) {
      result.push({
        key: `custom:${item.id}`,
        apn: item.apn,
        ipVersion: item.ipVersion,
        source: "custom",
        customID: item.id,
        username: item.username,
        hasPassword: item.hasPassword,
        proxy: item.proxy,
        mcc: item.mcc,
        mnc: item.mnc,
        roamingIPVersion: item.roamingIpVersion,
        authType: item.authType,
      });
    }
    return result;
  }, [customProfiles, modemProfiles]);

  function isActive(row: APNRow) {
    const activeAPN = policy?.apn || "";
    const activeIP = policy?.ipVersion || "IPV4V6";
    if (row.source === "automatic") return activeAPN === "";
    if (row.apn !== activeAPN || row.ipVersion !== activeIP) return false;
    const activeCustom = customProfiles.some((item) => item.apn === activeAPN && item.ipVersion === activeIP);
    return row.source === "custom" || !activeCustom;
  }

  async function enable(row: APNRow) {
    setPendingKey(row.key);
    try {
      const saved = await updateCardPolicy(iccid, { apn: row.apn, ipVersion: row.ipVersion });
      onSaved(saved);
      message.success(row.source === "automatic" ? t("已使用运营商自动 APN 配置") : t("APN 已启用"));
    } catch (error) {
      message.error(apiMessage(error) || t("启用 APN 失败"));
    } finally {
      setPendingKey("");
    }
  }

  function openEditor(profile?: CardAPNProfile) {
    setEditingProfile(profile || null);
    setNewAPN(profile?.apn || "");
    setNewUsername(profile?.username || "");
    setNewPassword("");
    setNewProxy(profile?.proxy || "");
    setNewMCC(profile?.mcc || "");
    setNewMNC(profile?.mnc || "");
    setNewIPVersion(profile?.ipVersion || "IPV4V6");
    setNewRoamingIPVersion(profile?.roamingIpVersion || "IP");
    setNewAuthType(profile?.authType || "NONE");
    setClearPassword(false);
    setEditorOpen(true);
  }

  async function saveEditor() {
    const cleanAPN = newAPN.trim();
    if (!cleanAPN || !APN_PATTERN.test(cleanAPN)) {
      message.warning(t("APN 只能包含字母、数字、点、下划线或连字符，且最长 100 个字符"));
      return;
    }
    if (newMCC && !/^\d{3}$/.test(newMCC)) {
      message.warning(t("MCC 必须是 3 位数字"));
      return;
    }
    if (newMNC && !/^\d{2,3}$/.test(newMNC)) {
      message.warning(t("MNC 必须是 2 或 3 位数字"));
      return;
    }
    setAdding(true);
    try {
      const payload = {
        apn: cleanAPN,
        username: newUsername.trim(),
        proxy: newProxy.trim(),
        mcc: newMCC.trim(),
        mnc: newMNC.trim(),
        ipVersion: newIPVersion,
        roamingIpVersion: newRoamingIPVersion,
        authType: newAuthType,
      };
      if (editingProfile) {
        await updateCardAPN(iccid, editingProfile.id, {
          ...payload,
          ...(newPassword ? { password: newPassword } : {}),
          clearPassword,
        });
      } else {
        await createCardAPN(iccid, { ...payload, password: newPassword });
      }
      setEditorOpen(false);
      await load();
      message.success(editingProfile ? t("自定义 APN 已修改") : t("自定义 APN 已添加，请点击启用后使用"));
    } catch (error) {
      message.error(apiMessage(error) || (editingProfile ? t("修改 APN 失败") : t("添加 APN 失败")));
    } finally {
      setAdding(false);
    }
  }

  async function remove(row: APNRow) {
    if (!row.customID) return;
    const confirmed = await confirmDialog(
      t("确定删除这个自定义 APN 配置吗？"),
      t("删除 APN"),
      { type: "danger", confirmVariant: "danger", confirmText: t("删除") },
    );
    if (!confirmed) return;
    setPendingKey(row.key);
    try {
      const wasActive = isActive(row);
      await deleteCardAPN(iccid, row.customID);
      if (wasActive && policy) {
        onSaved({ ...policy, apn: "", ipVersion: "IPV4V6" });
      }
      await load();
      message.success(wasActive ? t("APN 已删除，并恢复运营商自动配置") : t("自定义 APN 已删除"));
    } catch (error) {
      message.error(apiMessage(error) || t("删除 APN 失败"));
    } finally {
      setPendingKey("");
    }
  }

  function edit(row: APNRow) {
    const profile = customProfiles.find((item) => item.id === row.customID);
    if (profile) openEditor(profile);
  }

  const sourceLabel = (row: APNRow) => {
    if (row.source === "automatic") return t("默认");
    if (row.source === "modem") return t("模组已有");
    return t("自定义");
  };
  const protocolLabel = (value?: "IP" | "IPV6" | "IPV4V6") => value === "IP" ? "IPv4" : value === "IPV6" ? "IPv6" : value === "IPV4V6" ? "IPv4 / IPv6" : "—";

  return (
    <div className="rounded-lg border border-gray-200/70 bg-white/60 p-3 dark:border-white/10 dark:bg-black/10">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="text-sm font-semibold text-gray-800 dark:text-gray-100">{t("蜂窝 APN")}</div>
          <div className="mt-0.5 text-[11px] text-gray-500 dark:text-gray-400">
            {t("APN 列表和启用状态跟随当前 ICCID/Profile 保存")}
          </div>
        </div>
        <Button size="small" variant="primary" plain onClick={() => openEditor()}>
          {t("新增 APN")}
        </Button>
      </div>

      {loading ? (
        <div className="flex items-center justify-center gap-2 py-6 text-xs text-gray-400">
          <Spinner className="h-4 w-4 animate-spin" /> {t("正在读取 APN 列表...")}
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-gray-200/70 dark:border-white/10">
          <table className="w-full min-w-[1050px] text-left text-xs">
            <thead className="bg-gray-50 text-gray-500 dark:bg-white/5 dark:text-gray-400">
              <tr>
                <th className="px-3 py-2 font-semibold">APN</th>
                <th className="px-3 py-2 font-semibold">{t("账号 / 认证")}</th>
                <th className="px-3 py-2 font-semibold">MCC / MNC</th>
                <th className="px-3 py-2 font-semibold">{t("协议 / 漫游")}</th>
                <th className="px-3 py-2 font-semibold">Proxy</th>
                <th className="px-3 py-2 font-semibold">{t("来源")}</th>
                <th className="px-3 py-2 font-semibold">{t("状态")}</th>
                <th className="px-3 py-2 text-right font-semibold">{t("操作")}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100 dark:divide-white/10">
              {rows.map((row) => {
                const active = isActive(row);
                return (
                  <tr key={row.key} className={active ? "bg-sky-50/60 dark:bg-sky-500/5" : "bg-white/40 dark:bg-transparent"}>
                    <td className="px-3 py-2.5 font-mono text-gray-800 dark:text-gray-100">
                      {row.source === "automatic" ? t("运营商自动配置") : row.apn}
                      {row.cid ? <span className="ml-2 text-[10px] text-gray-400">CID {row.cid}</span> : null}
                    </td>
                    <td className="px-3 py-2.5 text-gray-600 dark:text-gray-300">
                      <div>{row.username || "—"}{row.hasPassword ? <span className="ml-1 text-[10px] text-gray-400">{t("已设密码")}</span> : null}</div>
                      {row.authType && row.authType !== "NONE" ? <div className="mt-0.5 text-[10px] text-gray-400">{row.authType.replace("PAP_OR_CHAP", "PAP / CHAP")}</div> : null}
                    </td>
                    <td className="px-3 py-2.5 text-gray-600 dark:text-gray-300">{row.mcc || "—"} / {row.mnc || "—"}</td>
                    <td className="px-3 py-2.5 text-gray-600 dark:text-gray-300"><div>{protocolLabel(row.ipVersion)}</div><div className="mt-0.5 text-[10px] text-gray-400">{t("漫游")}：{protocolLabel(row.roamingIPVersion)}</div></td>
                    <td className="max-w-[150px] truncate px-3 py-2.5 text-gray-600 dark:text-gray-300">{row.proxy || "—"}</td>
                    <td className="px-3 py-2.5"><Tag type={row.source === "custom" ? "primary" : "info"}>{sourceLabel(row)}</Tag></td>
                    <td className="px-3 py-2.5">{active ? <Tag type="success">{t("使用中")}</Tag> : <span className="text-gray-400">—</span>}</td>
                    <td className="whitespace-nowrap px-3 py-2.5 text-right">
                      <Button size="small" variant={active ? "default" : "primary"} plain={!active} disabled={active} loading={pendingKey === row.key} onClick={() => enable(row)}>
                        {active ? t("已启用") : t("启用")}
                      </Button>
                      {row.source === "custom" ? (
                        <>
                          <Button className="ml-1" size="small" onClick={() => edit(row)}>{t("修改")}</Button>
                          <Button className="ml-1" size="small" variant="danger" plain loading={pendingKey === row.key} onClick={() => remove(row)}>{t("删除")}</Button>
                        </>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {!deviceOnline ? <div className="mt-2 text-[11px] text-amber-600 dark:text-amber-400">{t("设备离线：自定义列表仍可管理，模组已有 APN 将在上线后读取")}</div> : null}
      <Modal
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        title={editingProfile ? t("修改 APN 配置") : t("新增 APN 配置")}
        width="max-w-4xl"
        closeOnOverlay={!adding}
        footer={
          <>
            <Button disabled={adding} onClick={() => setEditorOpen(false)}>{t("取消")}</Button>
            <Button variant="primary" loading={adding} onClick={saveEditor}>{editingProfile ? t("保存修改") : t("添加到列表")}</Button>
          </>
        }
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">APN *</span><Input value={newAPN} maxLength={100} onChange={(event) => setNewAPN(event.target.value)} placeholder="giffgaff.com" /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">{t("用户名")}</span><Input value={newUsername} maxLength={128} onChange={(event) => setNewUsername(event.target.value)} placeholder="gg" /></label>
          <label className="space-y-1">
            <span className="text-xs font-semibold text-gray-600 dark:text-gray-300">{t("密码")}</span>
            <Input type="password" value={newPassword} disabled={clearPassword} maxLength={128} onChange={(event) => setNewPassword(event.target.value)} placeholder={editingProfile?.hasPassword ? t("留空表示保持原密码") : "p"} />
          </label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">Proxy</span><Input value={newProxy} maxLength={255} onChange={(event) => setNewProxy(event.target.value)} placeholder={t("留空")} /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">MCC</span><Input inputMode="numeric" value={newMCC} maxLength={3} onChange={(event) => setNewMCC(event.target.value.replace(/\D/g, ""))} placeholder="234" /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">MNC</span><Input inputMode="numeric" value={newMNC} maxLength={3} onChange={(event) => setNewMNC(event.target.value.replace(/\D/g, ""))} placeholder="10" /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">{t("APN 协议")}</span><Select value={newIPVersion} onChange={(value) => setNewIPVersion(value as "IP" | "IPV6" | "IPV4V6")} options={[{ value: "IPV4V6", label: "IPv4 / IPv6" }, { value: "IP", label: "IPv4" }, { value: "IPV6", label: "IPv6" }]} /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">{t("APN 漫游协议")}</span><Select value={newRoamingIPVersion} onChange={(value) => setNewRoamingIPVersion(value as "IP" | "IPV6" | "IPV4V6")} options={[{ value: "IP", label: "IPv4" }, { value: "IPV4V6", label: "IPv4 / IPv6" }, { value: "IPV6", label: "IPv6" }]} /></label>
          <label className="space-y-1"><span className="text-xs font-semibold text-gray-600 dark:text-gray-300">{t("认证类型")}</span><Select value={newAuthType} onChange={(value) => setNewAuthType(value as "NONE" | "PAP" | "CHAP" | "PAP_OR_CHAP")} options={[{ value: "NONE", label: t("无") }, { value: "PAP", label: "PAP" }, { value: "CHAP", label: "CHAP" }, { value: "PAP_OR_CHAP", label: "PAP / CHAP" }]} /></label>
        </div>
        {editingProfile?.hasPassword ? (
          <div className="mt-4 flex items-center justify-between rounded-lg bg-gray-50 px-3 py-2 dark:bg-white/5">
            <div><div className="text-xs font-semibold text-gray-700 dark:text-gray-200">{t("清除已保存密码")}</div><div className="text-[11px] text-gray-400">{t("关闭时，密码输入框留空会保持原密码")}</div></div>
            <Switch checked={clearPassword} onChange={(value) => { setClearPassword(value); if (value) setNewPassword(""); }} />
          </div>
        ) : null}
      </Modal>
    </div>
  );
}
