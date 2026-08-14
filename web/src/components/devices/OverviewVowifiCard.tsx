import { useState } from "react";
import { cx } from "../../lib/utils";
import { StatusDot } from "../ui";
import { FieldRow } from "./FieldRow";
import type { DeviceDetail } from "./types";
import { tf, useI18n } from "../../lib/i18n";

export function OverviewVowifiCard({ device }: { device: DeviceDetail }) {
  const { t } = useI18n();
  const [expanded, setExpanded] = useState(false);
  const rt = device.vowifiRuntime;
  const items = [
    { key: "SIM", ready: rt?.simReady },
    { key: "Access", ready: rt?.accessReady },
    { key: "Tunnel", ready: rt?.tunnelReady },
    { key: "IMS", ready: rt?.imsReady },
    { key: "SMS", ready: rt?.smsReady },
  ];
  const flags = [rt?.simReady, rt?.accessReady, rt?.tunnelReady, rt?.imsReady, rt?.smsReady];
  const overall = !rt ? "off" : flags.every(Boolean) ? "ok" : flags.some(Boolean) ? "partial" : "off";
  const notReady = items.filter((i) => !i.ready).map((i) => i.key);
  const hasError = !!(rt?.lastErrorClass || rt?.lastReason);
  const eapRejected = rt?.lastErrorClass === "eap_authentication_rejected";
  const tone = overall === "partial" ? "warning" : overall === "ok" ? "success" : "danger";
  const showDetails = expanded || hasError;

  return (
    <>
      <div
        className={cx(
          "mb-3 flex items-center gap-2.5 rounded-xl border px-3.5 py-2.5",
          overall === "ok" && "border-emerald-200 bg-emerald-50 dark:border-emerald-500/25 dark:bg-emerald-500/10",
          overall === "partial" && "border-amber-200 bg-amber-50 dark:border-amber-500/25 dark:bg-amber-500/10",
          overall === "off" && "border-red-200 bg-red-50 dark:border-red-500/25 dark:bg-red-500/10",
        )}
      >
        <StatusDot tone={tone} size="sm" animated={overall !== "off"} />
        <div className="min-w-0">
          <div
            className={cx(
              "text-sm font-bold leading-tight",
              overall === "ok" && "text-emerald-700 dark:text-emerald-300",
              overall === "partial" && "text-amber-700 dark:text-amber-300",
              overall === "off" && "text-red-700 dark:text-red-300",
            )}
          >
            {overall === "ok" ? t("WiFi-Calling · 全部就绪") : overall === "partial" ? tf("{items} 未就绪", { items: notReady.join(" · ") }) : t("VoWiFi 未连接")}
          </div>
          {overall === "partial" && rt?.lastReason ? (
            <div className="mt-0.5 truncate text-xs text-amber-600 dark:text-amber-400">{rt.lastReason}</div>
          ) : null}
        </div>
      </div>
      <div className="mb-3">
        <div className="mb-1 flex gap-1">
          {items.map((item) => (
            <div
              key={item.key}
              className={cx(
                "h-1.5 flex-1 rounded-full",
                item.ready === true
                  ? "bg-emerald-500 dark:bg-emerald-400"
                  : item.ready === false
                    ? "bg-red-500 dark:bg-red-400"
                    : "bg-gray-200 dark:bg-white/10",
              )}
            />
          ))}
        </div>
        <div className="flex justify-between">
          {items.map((item) => (
            <span
              key={item.key}
              className={cx(
                "flex-1 text-center text-[10px]",
                item.ready === false ? "font-bold text-red-500 dark:text-red-400" : "text-gray-400 dark:text-gray-500",
              )}
            >
              {item.key}
            </span>
          ))}
        </div>
      </div>
      <div className="overflow-hidden rounded-lg border border-gray-200 dark:border-white/10">
        <button
          type="button"
          className="flex w-full items-center justify-between px-3 py-2 text-xs text-gray-500 transition-colors hover:bg-gray-50 dark:text-gray-400 dark:hover:bg-white/5"
          onClick={() => setExpanded((v) => !v)}
        >
          <span className="font-bold uppercase tracking-wider">{t("详情")}</span>
          <span>{showDetails ? "▴" : "▾"}</span>
        </button>
        {showDetails ? (
          <div className="space-y-1.5 border-t border-gray-100 px-3 pb-2 pt-2 text-sm text-gray-700 dark:border-white/5 dark:text-gray-200">
            {eapRejected ? (
              <div className="mb-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-800 dark:border-amber-500/25 dark:bg-amber-500/10 dark:text-amber-200">
                {t("已连接到运营商 ePDG，但 EAP-AKA 流程被拒绝。可能是初始身份、运营商 IKE/EAP 兼容性或订阅策略问题；请根据错误详情确认失败阶段。")}
              </div>
            ) : null}
            <FieldRow label={t("数据平面")} value={rt?.dataplaneMode || "--"} monospace />
            <FieldRow label={t("最后原因")} value={rt?.lastReason || "--"} />
            <FieldRow label={t("错误分类")} value={rt?.lastErrorClass || "--"} monospace copyable />
            {rt?.lastError ? <FieldRow label={t("错误详情")} value={rt.lastError} monospace copyable /> : null}
          </div>
        ) : null}
      </div>
    </>
  );
}
