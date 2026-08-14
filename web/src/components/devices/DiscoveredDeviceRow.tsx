import { cx } from "../../lib/utils";
import { Tag } from "../ui";
import type { DiscoveredDevice } from "../../types";
import { useI18n } from "../../lib/i18n";

export function DiscoveredDeviceRow({
  device,
  selected,
  modeLabel,
  isQmi,
  onSelect,
}: {
  device: DiscoveredDevice;
  selected: boolean;
  modeLabel: string;
  isQmi: boolean;
  onSelect: (d: DiscoveredDevice) => void;
}) {
  const { t } = useI18n();
  const degraded = !!device.degraded;
  const displayName = device.readerName || device.driverName || device.netInterface || device.controlPath || t("未知设备");
  const discoveryMessage = device.discoveryIssue === "pcsc_service_unavailable"
    ? t("系统已发现 USB 读卡器，但 PC/SC 服务未运行；请安装并启动 pcscd 后重新扫描。")
    : device.discoveryIssue === "pcsc_driver_missing"
      ? t("系统已发现 USB 读卡器，但 PC/SC 驱动未加载；请安装 libccid 或厂商驱动后重新扫描。")
      : "";
  return (
    <button
      type="button"
      aria-disabled={degraded}
      onClick={() => onSelect(device)}
      className={cx(
        "w-full rounded-xl border p-3 text-left",
        degraded && "cursor-not-allowed border-amber-200 bg-amber-50 opacity-85",
        !degraded && selected && "border-indigo-300 bg-indigo-50",
        !degraded && !selected && "border-gray-200 hover:bg-gray-50",
      )}
    >
      <div className="flex items-center gap-2 font-bold text-gray-800">
        <span>{displayName}</span>
        <Tag type={isQmi ? "success" : "warning"}>{modeLabel}</Tag>
      </div>
      <div className="mt-0.5 truncate text-xs text-gray-500">
        {device.hardwareKind === "pcsc"
          ? `USB: ${device.usbPath || "--"} · VID:PID ${device.vendorId.toString(16).padStart(4, "0")}:${device.productId.toString(16).padStart(4, "0")}`
          : `${device.controlPath} · AT: ${device.atPort || "--"} · IMEI: ${device.imei || "--"} · USB: ${device.usbPath || "--"}`}
      </div>
      {degraded ? <div className="mt-1 text-xs text-amber-700">{discoveryMessage || t("未找到可用的 AT 端口（串口可能仍在枚举），系统会自动重试；也可点击重新扫描。")}</div> : null}
    </button>
  );
}
