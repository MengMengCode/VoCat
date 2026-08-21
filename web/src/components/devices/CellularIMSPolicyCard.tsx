import { useCallback, useEffect, useState } from "react";
import { apiMessage } from "../../api";
import { useI18n } from "../../lib/i18n";
import { confirmDialog, message } from "../ui";
import { PolicySwitchCard } from "./PolicySwitchCard";
import { getCellularIMS, setCellularIMS, updateCardPolicy } from "./deviceActions";

interface CellularIMSPolicyCardProps {
  deviceId: string;
  iccid: string;
  enabled: boolean;
  managed: boolean;
  live: boolean;
  deviceOnline: boolean;
  vowifiEnabled: boolean;
  airplaneEnabled: boolean;
  compact?: boolean;
  onChanged: () => void | Promise<void>;
}

export function CellularIMSPolicyCard({ deviceId, iccid, enabled, managed, live, deviceOnline, vowifiEnabled, airplaneEnabled, compact, onChanged }: CellularIMSPolicyCardProps) {
  const { t } = useI18n();
  const [local, setLocal] = useState(enabled);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const [status, setStatus] = useState<Awaited<ReturnType<typeof getCellularIMS>> | null>(null);

  useEffect(() => setLocal(enabled), [enabled, iccid]);

  const loadStatus = useCallback(async () => {
    if (!live) {
      setStatus(null);
      return;
    }
    try {
      const status = await getCellularIMS(deviceId);
      setStatus(status);
    } catch {
      setStatus(null);
    }
  }, [deviceId, live]);

  useEffect(() => { void loadStatus(); }, [loadStatus]);

  const toggle = async (value: boolean) => {
    if (pending || !deviceOnline) return;
    const confirmed = await confirmDialog(
      <div className="space-y-2">
        {value && status?.registered ? (
          <p>{t("当前蜂窝 IMS 已正常注册，通常无需强制启用；继续操作仍会改为强制模式。")}</p>
        ) : null}
        {value && !status?.registered && status?.csKnown && status.csRegistered ? (
          <p>{t("当前已注册 CS 域，短信可能正在通过 CS 正常工作；强制 IMS 后能否注册取决于运营商、漫游网络和 MBN。")}</p>
        ) : null}
        {value && live && !status ? (
          <p>{t("无法读取当前 CS/IMS 状态，继续后将直接尝试应用强制 IMS 配置。")}</p>
        ) : null}
        {!value ? (
          <p>{t("关闭此开关只会恢复 MBN/运营商默认 IMS 行为，并不保证蜂窝 IMS 会被禁用。")}</p>
        ) : null}
        {!managed ? <p>{t("确认后 VoCat 将开始按此 ICCID 管理蜂窝 IMS 配置，切换其他卡时不会沿用本卡策略。")}</p> : null}
        {vowifiEnabled ? (
          <p>{t("当前 VoWiFi 已开启且蜂窝射频关闭；此设置会保存，但蜂窝 IMS 需在关闭 VoWiFi并恢复蜂窝射频后才能注册。")}</p>
        ) : airplaneEnabled ? (
          <p>{t("当前处于飞行模式；此设置会保存，但蜂窝 IMS 需在关闭飞行模式后才能注册。")}</p>
        ) : null}
        {!live ? <p>{t("此卡当前未激活或设备离线，配置将在此卡激活并上线后应用。")}</p> : null}
        <p className="font-medium text-amber-700 dark:text-amber-300">
          {live
            ? t("确认后将应用配置并重启模组，蜂窝数据、短信和通话可能短暂断联。")
            : t("此卡激活后应用配置时会重启模组，蜂窝数据、短信和通话可能短暂断联。")}
        </p>
      </div>,
      value ? t("确认强制启用蜂窝 IMS 短信") : t("确认恢复默认 IMS 行为"),
      { confirmText: value ? t("强制启用") : t("恢复默认"), type: "warning" },
    );
    if (!confirmed) return;
    const previous = local;
    setLocal(value);
    setPending(true);
    setFailed(false);
    try {
      if (live) {
        const status = await setCellularIMS(deviceId, value);
        setStatus(status);
        if (status.rebooting) message.success(t("IMS 配置已保存，模组正在重启"));
        else if (!status.changed) message.success(t("状态已一致，已跳过重启流程"));
      } else {
        await updateCardPolicy(iccid, { cellularImsEnabled: value });
        message.success(t("IMS 配置已保存，将在此卡激活后生效"));
      }
      await onChanged();
    } catch (error) {
      setLocal(previous);
      setFailed(true);
      message.error(apiMessage(error) || t("蜂窝 IMS 配置失败"));
      await onChanged();
    } finally {
      setPending(false);
    }
  };

  return <PolicySwitchCard
    compact={compact}
    title={t("强制启用蜂窝 IMS 短信")}
    subtitle={compact ? undefined : t("解决部分运营商无法收发短信的问题；该策略根据当前 ICCID/Profile 保存")}
    tone="indigo"
    checked={local}
    disabled={pending || !deviceOnline}
    pending={pending}
    failed={failed}
    onToggle={(value) => void toggle(value)}
  />;
}
