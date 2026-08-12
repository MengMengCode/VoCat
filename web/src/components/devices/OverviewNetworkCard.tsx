import { SettingsRegular } from "@fluentui/react-icons";
import { cx } from "../../lib/utils";
import { StatusDot } from "../ui";
import { FieldRow } from "./FieldRow";
import { isDeviceOnline, isRegistered, isRecoveringPhase, lifecycleLabel, signalLevel, signalTone } from "./shared";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { flagEmoji } from "../../lib/carrier";

const BAR_HEIGHTS = ["h-[28%]", "h-[46%]", "h-[64%]", "h-[82%]", "h-full"];
const TEXT_TONE = {
  green: "text-emerald-500 dark:text-emerald-400",
  amber: "text-amber-500 dark:text-amber-400",
  red: "text-red-500 dark:text-red-400",
  gray: "text-gray-400 dark:text-gray-500",
} as const;
const BAR_TONE = {
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  gray: "bg-gray-300 dark:bg-gray-600",
} as const;

export function OverviewNetworkCard({ device, onOpenOperatorSelection }: { device: DeviceDetail; onOpenOperatorSelection: () => void }) {
  const { t } = useI18n();
  const modem = device.modem;
  const online = isDeviceOnline(device);
	const cellularRegistered = isRegistered(device);
	// A persisted runtime may briefly describe the old session while disable is
	// being cleaned up. Desired policy is authoritative for the overview badge.
	const vowifiRegistered = !!device.vowifiEnabled && !!(device.vowifiActive || device.vowifiRuntime?.smsReady);
	const registered = cellularRegistered || vowifiRegistered;
	const radioOffForVowifi = vowifiRegistered && (modem?.operatingMode === 0 || modem?.operatingMode === 4 || device.flightMode);
	const tone = isRecoveringPhase(device.lifecyclePhase) ? "warning" : online ? (registered ? "success" : "warning") : "danger";

  let statusText: string;
  const phaseLabel = lifecycleLabel(device.lifecyclePhase);
  if (phaseLabel && device.lifecyclePhase !== "online" && device.lifecyclePhase !== "offline") statusText = phaseLabel;
  else if (online) {
    statusText = registered
      ? ""
      : device.registrationStateLabel === "searching"
        ? t("搜索网络中")
        : device.registrationStateLabel === "denied"
          ? t("驻网被拒")
          : t("未驻网");
  } else statusText = device.running ? t("控制面恢复中") : t("离线");

  const level = signalLevel(modem?.signalDbm);
  const sigTone = signalTone(modem?.signalDbm);
	const netMode = [modem?.networkDuplex, modem?.networkMode].filter(Boolean).join(" ");
	const cellularRegistrationText = modem?.regStatus === 5
	  ? t("已驻网（漫游）")
	  : modem?.regStatus === 1
		? t("已驻网")
		: device.registrationStateLabel === "searching"
		  ? t("正在搜索网络")
		  : device.registrationStateLabel === "denied"
			? t("驻网被拒")
			: t("未驻网");

  return (
    <>
      <div
        className={cx(
          "mb-3 flex items-center gap-2.5 rounded-xl border px-3.5 py-2.5",
          registered
            ? "border-emerald-200 bg-emerald-50 dark:border-emerald-500/25 dark:bg-emerald-500/10"
            : online
              ? "border-amber-200 bg-amber-50 dark:border-amber-500/25 dark:bg-amber-500/10"
              : "border-gray-200 bg-gray-100 dark:border-white/10 dark:bg-white/5",
        )}
      >
        <StatusDot tone={tone} size="sm" animated={registered} />
        <div className="min-w-0 flex-1">
          <div
            className={cx(
              "text-sm font-bold leading-tight",
              registered ? "text-emerald-700 dark:text-emerald-300" : online ? "text-amber-700 dark:text-amber-300" : "text-gray-500 dark:text-gray-400",
            )}
          >
			{vowifiRegistered ? (
			  <>{t("WiFi Calling 已注册")}</>
			) : registered ? (
              <>
                {modem?.operatorCountryCode ? `${flagEmoji(modem.operatorCountryCode)} ` : ""}{modem?.operator || "--"}{" "}
                {modem?.networkMode ? <span className="opacity-70">· {netMode}</span> : null}
              </>
            ) : (
              statusText
            )}
          </div>
        </div>
        <button
          type="button"
          onClick={onOpenOperatorSelection}
          className="rounded p-1 transition-colors hover:bg-black/5 dark:hover:bg-white/10"
          title={t("网络选择设置")}
        >
          <SettingsRegular className="h-5 w-5 text-gray-500 dark:text-gray-400" />
        </button>
      </div>
      <div className="mb-3 rounded-xl border border-gray-200 px-3.5 py-3 dark:border-white/10">
        <div className="mb-1.5 text-[10px] font-bold uppercase tracking-wider text-gray-400">{t("信号强度")}</div>
        <div className="flex items-center gap-3">
          <div>
            <div className="flex items-baseline gap-1">
              <span className={cx("text-2xl font-extrabold tabular-nums leading-none", TEXT_TONE[sigTone])}>
				{radioOffForVowifi ? "--" : (modem?.signalDbm ?? "--")}
              </span>
              <span className="text-xs text-gray-400">dBm</span>
            </div>
            <div className="mt-1 text-[10px] text-gray-400">
			  {radioOffForVowifi
				? t("蜂窝射频已关闭（VoWiFi 接管）")
				: <>RSRP {modem?.signalRsrp ?? "--"} · RSRQ {modem?.signalRsrq ?? "--"} · SINR {modem?.signalSinr ?? "--"}
					{modem?.nr5gSignalSinr !== undefined ? ` ·NR5G SINR ${modem.nr5gSignalSinr}` : null}</>}
            </div>
          </div>
          <div className="ml-auto flex h-7 items-end gap-0.5">
            {[1, 2, 3, 4, 5].map((i) => (
              <div key={i} className={cx("w-1.5 rounded-sm", BAR_HEIGHTS[i - 1], i <= level ? BAR_TONE[sigTone] : "bg-gray-200 dark:bg-white/10")} />
            ))}
          </div>
        </div>
      </div>
      <div className="space-y-1.5 text-sm text-gray-700 dark:text-gray-200">
        <FieldRow label={t("网络模式")} value={netMode || "--"} monospace />
        <FieldRow label={t("频段")} value={modem?.radioBand || "--"} monospace />
        <FieldRow label={t("信道")} value={modem?.radioChannel ? String(modem.radioChannel) : "--"} monospace />
		<FieldRow label={t("注册状态")} value={vowifiRegistered ? t("WiFi Calling 已注册") : cellularRegistrationText} monospace />
      </div>
    </>
  );
}
