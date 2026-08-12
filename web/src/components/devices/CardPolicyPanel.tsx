import { useEffect, useState } from "react";
import { CardUiRegular } from "@fluentui/react-icons";
import { Button, Input, Tag, message } from "../ui";
import { PolicySwitchCard } from "./PolicySwitchCard";
import { CardPolicyAPN } from "./CardPolicyAPN";
import { useCardPolicyToggles } from "./useCardPolicyToggles";
import { enableVoWiFi, disableVoWiFi, setFlightMode, updateCardPolicy } from "./deviceActions";
import type { CardPolicy } from "../../types";
import { useI18n } from "../../lib/i18n";
import { apiMessage } from "../../api";

export interface CardPolicyPanelProps {
  deviceId: string;
  iccid?: string;
  policy: CardPolicy | null;
  deviceOnline: boolean;
  onPolicyChanged: () => void | Promise<void>;
	wifiCallingOnly?: boolean;
}

export function CardPolicyPanel({ deviceId, iccid, policy, deviceOnline, onPolicyChanged, wifiCallingOnly = false }: CardPolicyPanelProps) {
  const { t } = useI18n();
  const operable = deviceOnline && !!iccid;
  const currentPolicy = policy?.iccid === iccid ? policy : null;
  const [customPhoneNumber, setCustomPhoneNumber] = useState(currentPolicy?.customPhoneNumber || "");
  const [phoneSaving, setPhoneSaving] = useState(false);
  const flags = currentPolicy
    ? { vowifiEnabled: currentPolicy.vowifiEnabled, airplaneEnabled: currentPolicy.airplaneEnabled }
    : null;

  const toggles = useCardPolicyToggles(flags, {
    applyVoWiFi: (value) => (deviceId ? (value ? enableVoWiFi(deviceId) : disableVoWiFi(deviceId)) : Promise.resolve({ ok: false })),
    applyAirplane: (value) => (deviceId ? setFlightMode(deviceId, value) : Promise.resolve({ ok: false })),
    onChanged: onPolicyChanged,
  });

  const isManual = currentPolicy?.source === "user" || currentPolicy?.source === "manual";
  const sourceLabel = currentPolicy ? (isManual ? t("手动设置") : t("自动默认")) : "";
  const { local } = toggles;
  const savedPhoneNumber = currentPolicy?.customPhoneNumber || "";
  const phoneChanged = customPhoneNumber.trim() !== savedPhoneNumber;

  useEffect(() => {
    setCustomPhoneNumber(currentPolicy?.customPhoneNumber || "");
  }, [iccid, currentPolicy?.customPhoneNumber]);

  const saveCustomPhoneNumber = async () => {
    if (!iccid || phoneSaving || !phoneChanged) return;
    setPhoneSaving(true);
    try {
      const saved = await updateCardPolicy(iccid, { customPhoneNumber: customPhoneNumber.trim() });
      setCustomPhoneNumber(saved.customPhoneNumber || "");
      message.success(saved.customPhoneNumber ? t("自定义手机号已保存") : t("已恢复显示系统读取的号码"));
      await onPolicyChanged();
    } catch (error) {
      message.error(apiMessage(error) || t("保存自定义手机号失败"));
    } finally {
      setPhoneSaving(false);
    }
  };

  return (
    <div>
      <div className="mb-4 flex items-center gap-3">
        <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 dark:bg-indigo-500/10 dark:text-indigo-400">
          <CardUiRegular className="text-[22px]" />
        </div>
        <div>
          <div className="text-lg font-bold text-gray-900 dark:text-white">{t("卡策略")}</div>
		  <div className="text-xs text-gray-500 dark:text-gray-400">{wifiCallingOnly ? t("USB SIM 读卡器仅用于 WiFi Calling，策略跟随 ICCID 保存") : t("VoWiFi / 飞行模式 开关跟着 SIM 卡走，切换即时生效")}</div>
        </div>
      </div>
      {!iccid ? (
        <div className="ui-panel-muted p-4 text-center text-sm text-gray-500 dark:text-gray-400">{t("设备尚未识别到 SIM 卡 ICCID，策略不可操作")}</div>
      ) : null}
      {iccid && !deviceOnline ? (
        <div className="mb-3 rounded-lg bg-yellow-50 px-3 py-2 text-xs text-yellow-700 dark:bg-yellow-900/20 dark:text-yellow-300">
          {t("设备离线，策略仅展示，切换操作已禁用")}
        </div>
      ) : null}
      {iccid ? (
        <div className="space-y-3">
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <div className="ui-panel-muted flex min-w-0 items-center justify-between gap-3 p-3">
              <div className="min-w-0">
                <div className="mb-0.5 text-xs font-bold uppercase tracking-wider text-gray-500">{t("当前卡 ICCID")}</div>
                <div className="truncate font-mono text-sm text-gray-800 dark:text-gray-100" title={iccid}>{iccid}</div>
              </div>
              {sourceLabel ? <Tag type={isManual ? "primary" : "info"}>{sourceLabel}</Tag> : null}
            </div>
            <div className="ui-panel-muted p-3">
              <div className="mb-1.5 text-xs font-bold uppercase tracking-wider text-gray-500">{t("自定义手机号")}</div>
              <div className="flex items-center gap-2">
                <Input
                  value={customPhoneNumber}
                  onChange={(event) => setCustomPhoneNumber(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") void saveCustomPhoneNumber();
                  }}
                  placeholder={t("请输入手机号（可留空）")}
                  inputMode="tel"
                  maxLength={32}
                  disabled={phoneSaving}
                  aria-label={t("自定义手机号")}
                />
                <Button
                  variant="primary"
                  size="small"
                  className="shrink-0 !border-0"
                  loading={phoneSaving}
                  disabled={!phoneChanged}
                  onClick={() => void saveCustomPhoneNumber()}
                >
                  {t("保存")}
                </Button>
              </div>
              <div className="mt-1.5 text-[11px] leading-4 text-gray-500 dark:text-gray-400">
                {t("支持开头的 + 和 3-20 位数字；留空时显示系统从 SIM/网络读取的号码")}
              </div>
            </div>
          </div>
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
			<PolicySwitchCard
              title="VoWiFi"
              subtitle={t("启用时强制关闭蜂窝射频；关闭 VoWiFi 后仍保持飞行模式")}
              tone="orange"
              checked={local.vowifiEnabled}
              disabled={!operable || toggles.vowifiPending}
              pending={toggles.vowifiPending}
              failed={toggles.vowifiFailed}
              onToggle={toggles.onVoWiFiToggle}
            />
			{!wifiCallingOnly ? <PolicySwitchCard
              title={t("飞行模式")}
              subtitle={t("只有手动关闭此开关才允许设备连接基站")}
              tone="indigo"
              checked={local.airplaneEnabled}
              disabled={!operable || local.vowifiEnabled || toggles.airplanePending}
              pending={toggles.airplanePending}
              failed={toggles.airplaneFailed}
              onToggle={toggles.onAirplaneToggle}
			/> : null}
          </div>
		  {!wifiCallingOnly ? <CardPolicyAPN
            deviceId={deviceId}
            iccid={iccid}
            policy={currentPolicy}
            deviceOnline={deviceOnline}
            onSaved={onPolicyChanged}
		  /> : null}
        </div>
      ) : null}
    </div>
  );
}
