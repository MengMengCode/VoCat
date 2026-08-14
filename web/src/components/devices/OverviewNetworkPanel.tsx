import { useEffect, useState } from "react";
import { FieldRow } from "./FieldRow";
import type { DeviceDetail } from "./types";
import { useI18n } from "../../lib/i18n";
import { api, apiMessage } from "../../api";
import { Button, message } from "../ui";
import { CountryFlag } from "../CountryFlag";

interface PublicIPInfo {
  detected?: boolean;
  ip: string;
  countryCode: string;
  region?: string;
  city?: string;
  organization?: string;
}

export interface OverviewNetworkPanelProps {
  device: DeviceDetail;
  trafficMinuteRx: string;
  trafficMinuteTx: string;
  trafficSpeedRx: string;
  trafficSpeedTx: string;
}

export function OverviewNetworkPanel({ device, trafficMinuteRx, trafficMinuteTx, trafficSpeedRx, trafficSpeedTx }: OverviewNetworkPanelProps) {
  const { t, lang } = useI18n();
  const developerActive = !!device.developerEnabled;
  const [publicIP, setPublicIP] = useState<PublicIPInfo | null>(null);
  const [detectingIP, setDetectingIP] = useState(false);
  const traffic = device.traffic || {};
  const metaStatus = device.trafficMeta?.status;
  const sampleNote = metaStatus === "waiting_sample" ? t("等待采样") : metaStatus === "stale" ? t("采样中断") : "";
  const off = !device.networkEnabled;

  const minuteRx = trafficMinuteRx || sampleNote || traffic.rx;
  const minuteTx = trafficMinuteTx || sampleNote || traffic.tx;
  const speedRx = trafficSpeedRx || sampleNote || traffic.rate || "--";
  const speedTx = trafficSpeedTx || sampleNote || traffic.rateTx || "--";

  useEffect(() => {
    let cancelled = false;
    setPublicIP(null);
    if (!developerActive) return () => { cancelled = true; };
    api<PublicIPInfo>(`/devices/${encodeURIComponent(device.id)}/network/public-ip`)
      .then((info) => {
        if (!cancelled) setPublicIP(info.detected ? info : null);
      })
      .catch(() => {
        if (!cancelled) setPublicIP(null);
      });
    return () => { cancelled = true; };
  }, [developerActive, device.id, device.interface, device.networkEnabled, device.modem?.iccid]);

  async function detectPublicIP() {
    setDetectingIP(true);
    try {
      const info = await api<PublicIPInfo>(`/devices/${encodeURIComponent(device.id)}/network/public-ip`, { method: "POST", body: {} });
      setPublicIP(info);
    } catch (error) {
      message.error(apiMessage(error) || t("公网 IP 检测失败"));
    } finally {
      setDetectingIP(false);
    }
  }

  let countryName = publicIP?.countryCode || "";
  if (publicIP?.countryCode) {
    try {
      countryName = new Intl.DisplayNames([lang === "zh" ? "zh-CN" : "en"], { type: "region" }).of(publicIP.countryCode) || publicIP.countryCode;
    } catch {
      countryName = publicIP.countryCode;
    }
  }
  const location = publicIP
    ? [countryName, publicIP.region, publicIP.city].filter((value, index, values) => value && values.indexOf(value) === index).join(" · ")
    : "";

  if (!developerActive) {
    return null;
  }

  return (
    <div className="ui-panel-muted p-4">
      <div className="mb-2 text-xs font-bold uppercase tracking-wider text-gray-500">{t("网络")}</div>
      <div className="space-y-1.5 text-sm text-gray-700 dark:text-gray-200">
        <div className="flex w-full min-w-0 items-center justify-between gap-3">
          <span className="shrink-0 whitespace-nowrap text-gray-500">{t("公网 IP")}</span>
          <div className="flex min-w-0 items-center justify-end gap-2">
            <span className="truncate font-mono" title={publicIP?.ip || ""}>{publicIP?.ip || "-"}</span>
            <Button size="small" loading={detectingIP} disabled={off} onClick={() => void detectPublicIP()}>{t("检测")}</Button>
          </div>
        </div>
        <FieldRow
          label={t("国家/地区")}
          value={publicIP ? location : "-"}
          prefix={publicIP ? <CountryFlag countryCode={publicIP.countryCode} /> : null}
        />
        {off ? (
          <div className="flex items-center justify-center p-6 text-sm text-gray-400">{t("数据未开启")}</div>
        ) : (
          <>
          <FieldRow label={t("内网 IPv4")} value={device.privateIp} monospace copyable />
          <FieldRow label={t("近1分钟上传")} value={minuteTx} monospace />
          <FieldRow label={t("近1分钟下载")} value={minuteRx} monospace />
          <FieldRow label={t("实时下载速率")} value={speedRx} monospace />
          <FieldRow label={t("实时上传速率")} value={speedTx} monospace />
          </>
        )}
      </div>
    </div>
  );
}
