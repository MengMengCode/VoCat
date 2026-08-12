import { useCallback, useEffect, useState } from "react";
import { Button, Spinner } from "../ui";
import { PolicySwitchCard } from "./PolicySwitchCard";
import { CardPolicyAPN } from "./CardPolicyAPN";
import { useCardPolicyToggles } from "./useCardPolicyToggles";
import { getCardPolicy, putCardPolicy, enableVoWiFi, disableVoWiFi, setFlightMode } from "./deviceActions";
import type { CardPolicy } from "../../types";
import { useI18n } from "../../lib/i18n";

export interface EsimCardPolicyInlineProps {
  deviceId: string;
  iccid: string;
  isActiveCard: boolean;
  deviceOnline: boolean;
  onPolicyChanged: () => void;
}

export function EsimCardPolicyInline({ deviceId, iccid, isActiveCard, deviceOnline, onPolicyChanged }: EsimCardPolicyInlineProps) {
  const { t } = useI18n();
  const [policy, setPolicy] = useState<CardPolicy | null>(null);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(false);

  const mode = isActiveCard && deviceOnline ? "live" : "stored";
  const noteText = mode === "live" ? "" : deviceOnline ? t("改动将在此卡激活后生效") : t("设备离线，改动已保存，激活/上线后生效");
  const flags = policy
    ? { vowifiEnabled: policy.vowifiEnabled, airplaneEnabled: policy.airplaneEnabled }
    : null;

  const load = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      const data = await getCardPolicy(iccid);
      setPolicy(data);
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, [iccid]);

  useEffect(() => {
    load();
  }, [load]);

  const toggles = useCardPolicyToggles(flags, {
    applyVoWiFi: (value, next) => (mode === "stored" ? putCardPolicy(iccid, next) : value ? enableVoWiFi(deviceId) : disableVoWiFi(deviceId)),
    applyAirplane: (value, next) => (mode === "stored" ? putCardPolicy(iccid, next) : setFlightMode(deviceId, value)),
    onChanged: onPolicyChanged,
  });
  const { local } = toggles;

  return (
    <div className="space-y-3 rounded-lg bg-gray-50/60 px-4 py-3 dark:bg-white/5">
      {loading ? (
        <div className="flex items-center gap-1 text-xs text-gray-400">
          <Spinner className="h-3.5 w-3.5 animate-spin" />
          {t("正在加载策略...")}
        </div>
      ) : failed ? (
        <div className="flex items-center gap-2 text-xs text-orange-500">
          {t("策略加载失败")}
          <Button variant="text" size="small" onClick={load}>
            {t("重试")}
          </Button>
        </div>
      ) : (
        <>
          {noteText ? <div className="text-[11px] text-amber-600 dark:text-amber-400">{noteText}</div> : null}
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
            <PolicySwitchCard
              compact
              title="VoWiFi"
              checked={local.vowifiEnabled}
              disabled={toggles.vowifiPending}
              pending={toggles.vowifiPending}
              failed={toggles.vowifiFailed}
              onToggle={toggles.onVoWiFiToggle}
            />
            <PolicySwitchCard
              compact
              title={t("飞行")}
              checked={local.airplaneEnabled}
              disabled={local.vowifiEnabled || toggles.airplanePending}
              pending={toggles.airplanePending}
              failed={toggles.airplaneFailed}
              onToggle={toggles.onAirplaneToggle}
            />
          </div>
          <CardPolicyAPN
            deviceId={deviceId}
            iccid={iccid}
            policy={policy}
            deviceOnline={deviceOnline}
            onSaved={(saved) => {
              setPolicy(saved);
              onPolicyChanged();
            }}
          />
        </>
      )}
    </div>
  );
}
