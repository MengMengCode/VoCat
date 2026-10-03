import { useEffect, useRef, useState } from "react";
import { ArrowDownloadRegular } from "@fluentui/react-icons";
import { api, apiMessage } from "../../api";
import type { DeviceListItem, DevicesResponse } from "../../types";
import { tf, useI18n } from "../../lib/i18n";
import { Button, Input, Modal, Select } from "../ui";
import { buildSMSExportQuery, readSMSExportDownload, type SMSExportFormat } from "./smsExport";

interface SmsExportModalProps {
  devices: DeviceListItem[];
  defaultDeviceId: string;
  onClose: () => void;
}

export function SmsExportModal({ devices, defaultDeviceId, onClose }: SmsExportModalProps) {
  const { t } = useI18n();
  const [deviceId, setDeviceId] = useState(defaultDeviceId || "all");
  const [format, setFormat] = useState<SMSExportFormat>("json");
  const [timeRange, setTimeRange] = useState<"all" | "range">("all");
  const [startDate, setStartDate] = useState("");
  const [endDate, setEndDate] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState<{ count: number; filename: string } | null>(null);
  const request = useRef<AbortController | null>(null);

  const [exportDevices, setExportDevices] = useState(devices);
  const [devicesError, setDevicesError] = useState(false);

  useEffect(() => () => { request.current?.abort(); }, []);
  useEffect(() => {
    const controller = new AbortController();
    // The SMS page lists running devices for sending. Archives must also
    // allow selecting configured devices that are offline or stopped.
    void api<DevicesResponse>("/devices", { signal: controller.signal }).then((response) => {
      if (!controller.signal.aborted) setExportDevices(response.devices || []);
    }).catch(() => {
      if (!controller.signal.aborted) setDevicesError(true);
    });
    return () => controller.abort();
  }, []);

  const deviceOptions = [
    { value: "all", label: t("全部设备") },
    ...exportDevices.map((device) => ({ value: device.id, label: device.name ? `${device.name} (${device.id})` : device.id })),
  ];
  // Retain an explicit selection if a device disappears during polling. Never
  // silently turn a specific-device export into an all-device export.
  if (deviceId !== "all" && !exportDevices.some((device) => device.id === deviceId)) {
    deviceOptions.push({ value: deviceId, label: deviceId });
  }

  function changed() {
    setError("");
    setResult(null);
  }

  function close() {
    request.current?.abort();
    onClose();
  }

  async function download() {
    if (request.current) return;
    setError("");
    setResult(null);
    let query: URLSearchParams;
    try {
      query = buildSMSExportQuery({ deviceId, format, timeRange, startDate, endDate });
    } catch (e) {
      setError(t(apiMessage(e)));
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    try {
      const response = await api<Response>(`/sms/export?${query}`, { raw: true, signal: controller.signal, cache: "no-store" });
      const archive = await readSMSExportDownload(response, format);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(archive.blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = archive.filename;
      document.body.appendChild(link);
      try {
        link.click();
      } finally {
        link.remove();
        // Give the browser time to begin consuming the download before revoke.
        window.setTimeout(() => URL.revokeObjectURL(url), 30_000);
      }
      setResult({ count: archive.count, filename: archive.filename });
    } catch (e) {
      if (!controller.signal.aborted) setError(t(apiMessage(e)));
    } finally {
      if (request.current === controller) request.current = null;
      if (!controller.signal.aborted) setBusy(false);
    }
  }

  return (
    <Modal
      open
      onClose={close}
      title={t("导出短信")}
      footer={<>
        <Button onClick={close}>{busy ? t("取消导出") : t("关闭")}</Button>
        <Button variant="primary" loading={busy} icon={<ArrowDownloadRegular />} onClick={() => void download()}>
          {busy ? t("正在生成导出文件") : t("下载")}
        </Button>
      </>}
    >
      <div className="mt-2 space-y-4">
        <div>
          <div className="mb-1.5 text-sm font-medium text-gray-700 dark:text-gray-300">{t("导出设备")}</div>
          <Select value={deviceId} disabled={busy} options={deviceOptions} onChange={(value) => { setDeviceId(value); changed(); }} />
          {devicesError && <p role="alert" className="mt-2 text-xs text-amber-700 dark:text-amber-300">{t("设备列表加载失败，仍可按当前选择导出。")}</p>}
        </div>
        <div>
          <div className="mb-1.5 text-sm font-medium text-gray-700 dark:text-gray-300">{t("时间范围")}</div>
          <Select value={timeRange} disabled={busy} options={[{ value: "all", label: t("全部时间") }, { value: "range", label: t("指定日期范围") }]} onChange={(value) => { setTimeRange(value as "all" | "range"); changed(); }} />
        </div>
        {timeRange === "range" && (
          <div>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <label className="block space-y-1.5 text-sm font-medium text-gray-700 dark:text-gray-300">
                <span>{t("开始日期")}</span>
                <Input type="date" value={startDate} disabled={busy} max={endDate || undefined} onChange={(e) => { setStartDate(e.target.value); changed(); }} />
              </label>
              <label className="block space-y-1.5 text-sm font-medium text-gray-700 dark:text-gray-300">
                <span>{t("结束日期")}</span>
                <Input type="date" value={endDate} disabled={busy} min={startDate || undefined} onChange={(e) => { setEndDate(e.target.value); changed(); }} />
              </label>
            </div>
            <p className="mt-2 text-xs text-gray-500 dark:text-gray-400">{t("按浏览器本地时区筛选，包含开始和结束当天。")}</p>
          </div>
        )}
        <div>
          <div className="mb-1.5 text-sm font-medium text-gray-700 dark:text-gray-300">{t("文件格式")}</div>
          <Select value={format} disabled={busy} options={[{ value: "json", label: t("JSON（完整记录）") }, { value: "html", label: t("HTML（离线阅读）") }]} onChange={(value) => { setFormat(value as SMSExportFormat); changed(); }} />
          <p className="mt-2 text-xs text-gray-500 dark:text-gray-400">{t("JSON 时间使用 UTC；HTML 按导出时的浏览器时区显示，并标注时区。")}</p>
        </div>
        <p className="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">{t("文件包含短信正文、号码、设备和 SIM 标识，可能含验证码。不会脱敏或加密，请妥善保管。")}</p>
        {error && <p role="alert" className="text-sm text-red-600 dark:text-red-400">{error}</p>}
        {result && <div role="status" className="space-y-1 text-sm text-gray-600 dark:text-gray-300">
          <p>{tf("已生成 {count} 条短信的导出文件，请在浏览器下载中确认保存。", { count: result.count })}</p>
          {result.count === 0 && <p>{t("所选范围没有短信，已生成空归档。")}</p>}
          <p className="break-all text-xs">{result.filename}</p>
        </div>}
      </div>
    </Modal>
  );
}
