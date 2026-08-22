import { useCallback, useEffect, useRef, useState } from "react";
import { ArrowSyncRegular } from "@fluentui/react-icons";
import { Tag, type TagType } from "../ui/Tag";
import { cx } from "../../lib/utils";
import { useI18n } from "../../lib/i18n";
import { getCellularIMS, type CellularIMSStatus } from "./deviceActions";
import { isDeviceOnline } from "./shared";
import type { DeviceDetail } from "./types";

interface SMSChannelStatusRowProps {
  device: DeviceDetail;
  onRefreshOverview: () => void | Promise<void>;
}

interface DisplayStatus {
  label: string;
  tone: TagType;
}

export function SMSChannelStatusRow({ device, onRefreshOverview }: SMSChannelStatusRowProps) {
  const { t } = useI18n();
  const [cellular, setCellular] = useState<CellularIMSStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const requestSequence = useRef(0);

  const iccid = (device.modem?.iccid || "").trim();
  const usesVoWiFi = device.deviceType === "usb_sim_reader" || !!device.vowifiEnabled;
  const canProbeCellular = !usesVoWiFi && isDeviceOnline(device) && !!iccid && device.modem?.simInserted !== false;

  const probeCellular = useCallback(async () => {
    if (!canProbeCellular) return;
    const sequence = ++requestSequence.current;
    setLoading(true);
    try {
      const next = await getCellularIMS(device.id);
      if (sequence === requestSequence.current) setCellular(next);
    } catch {
      if (sequence === requestSequence.current) setCellular(null);
    } finally {
      if (sequence === requestSequence.current) setLoading(false);
    }
  }, [canProbeCellular, device.id]);

  // The overview tab is conditionally mounted, so this runs once when users
  // return to it and again when the active device/SIM/profile changes.
  useEffect(() => {
    requestSequence.current += 1;
    setCellular(null);
    setLoading(false);
    if (canProbeCellular) void probeCellular();
    return () => {
      requestSequence.current += 1;
    };
  }, [canProbeCellular, device.id, iccid, device.activeEsimProfileName, probeCellular]);

  const refresh = async () => {
    if (loading) return;
    if (usesVoWiFi) {
      setLoading(true);
      try {
        await onRefreshOverview();
      } finally {
        setLoading(false);
      }
      return;
    }
    await probeCellular();
  };

  let display: DisplayStatus;
  if (usesVoWiFi) {
    if (device.vowifiRuntime?.smsReady) display = { label: t("已就绪(VoWiFi)"), tone: "success" };
    else if (device.vowifiRuntime?.imsReady) display = { label: t("已注册(IMS)"), tone: "success" };
    else if (device.vowifiRuntime?.enabled) display = { label: t("未注册短信域"), tone: "warning" };
    else display = { label: t("状态未知"), tone: "info" };
  } else if (!canProbeCellular || !cellular) {
    display = { label: t("状态未知"), tone: "info" };
  } else if (cellular.registered && cellular.csRegistered) {
    display = { label: t("已注册(CS, IMS)"), tone: "success" };
  } else if (cellular.registered) {
    display = { label: t("已注册(IMS)"), tone: "success" };
  } else if (cellular.csRegistered) {
    display = { label: t("已注册(CS)"), tone: "success" };
  } else if (cellular.csKnown && cellular.supported) {
    display = { label: t("未注册短信域"), tone: "warning" };
  } else {
    display = { label: t("状态未知"), tone: "info" };
  }

  return (
    <div className="flex w-full min-w-0 items-center justify-between gap-3">
      <span className="shrink-0 whitespace-nowrap text-gray-500">{t("短信通道")}</span>
      <div className="flex min-w-0 items-center justify-end gap-1.5">
        <Tag type={display.tone}>{display.label}</Tag>
        <button
          type="button"
          onClick={() => void refresh()}
          disabled={loading}
          title={t("刷新短信通道状态")}
          aria-label={t("刷新短信通道状态")}
          className="rounded p-1 text-gray-400 transition-colors hover:bg-black/5 hover:text-gray-600 disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-white/10 dark:hover:text-gray-200"
        >
          <ArrowSyncRegular className={cx("h-4 w-4", loading && "animate-spin")} />
        </button>
      </div>
    </div>
  );
}
