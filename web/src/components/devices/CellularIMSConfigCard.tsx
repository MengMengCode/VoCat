import { useCallback, useEffect, useState } from "react";
import { apiMessage } from "../../api";
import { useI18n } from "../../lib/i18n";
import { Button, Select, Tag, confirmDialog, message } from "../ui";
import {
  getCellularIMS,
  setCellularIMSMode,
  type CellularIMSMode,
  type CellularIMSStatus,
} from "./deviceActions";

interface CellularIMSConfigCardProps {
  deviceId: string;
  deviceOnline: boolean;
}

const MODE_OPTIONS: Array<{ value: CellularIMSMode; label: string }> = [
  { value: "mbn_default", label: "跟随 MBN / 运营商默认（推荐）" },
  { value: "force_enabled", label: "强制启用整个 IMS" },
  { value: "force_disabled", label: "强制关闭整个 IMS" },
];

export function CellularIMSConfigCard({ deviceId, deviceOnline }: CellularIMSConfigCardProps) {
  const { t } = useI18n();
  const [status, setStatus] = useState<CellularIMSStatus | null>(null);
  const [selectedMode, setSelectedMode] = useState<CellularIMSMode>("mbn_default");
  const [loading, setLoading] = useState(false);
  const [applying, setApplying] = useState(false);
  const [loadError, setLoadError] = useState("");

  const load = useCallback(async () => {
    setLoadError("");
    if (!deviceOnline) {
      setStatus(null);
      return;
    }
    setLoading(true);
    try {
      const next = await getCellularIMS(deviceId);
      setStatus(next);
      setSelectedMode(next.mode);
    } catch (error) {
      setStatus(null);
      setLoadError(apiMessage(error) || t("读取蜂窝 IMS 模组配置失败"));
    } finally {
      setLoading(false);
    }
  }, [deviceId, deviceOnline, t]);

  useEffect(() => { void load(); }, [load]);

  const apply = async () => {
    if (!deviceOnline || applying || selectedMode === status?.mode) return;
    const forceDisabled = selectedMode === "force_disabled";
    const confirmed = await confirmDialog(
      <div className="space-y-2">
        {selectedMode === "mbn_default" ? (
          <p>{t("将清除 VoCat 的 IMS 强制覆盖，改由当前 MBN / 运营商配置决定是否启用 IMS。")}</p>
        ) : selectedMode === "force_enabled" ? (
          <p>{t("这会强制启用模组的整个蜂窝 IMS，而不只是 IMS 短信；VoLTE 等语音功能也会受到影响。")}</p>
        ) : (
          <p className="font-semibold text-red-600 dark:text-red-400">
            {t("这会强制关闭整个蜂窝 IMS，可能导致 VoLTE、IMS 短信及 IMS 语音不可用。")}
          </p>
        )}
        <p className="font-medium text-amber-700 dark:text-amber-300">
          {t("配置变化后将完整重启模组，蜂窝数据、短信和通话会短暂中断。")}
        </p>
      </div>,
      forceDisabled ? t("确认强制关闭整个 IMS") : t("确认修改蜂窝 IMS 配置"),
      { confirmText: forceDisabled ? t("强制关闭") : t("应用并重启"), type: forceDisabled ? "danger" : "warning" },
    );
    if (!confirmed) return;

    setApplying(true);
    try {
      const next = await setCellularIMSMode(deviceId, selectedMode);
      setStatus(next);
      setSelectedMode(next.mode);
      message.success(next.rebooting ? t("IMS 配置已写入，模组正在重启") : t("模组 IMS 配置已经一致"));
    } catch (error) {
      message.error(apiMessage(error) || t("蜂窝 IMS 模组配置失败"));
      await load();
    } finally {
      setApplying(false);
    }
  };

  return (
    <div className="ui-panel-muted space-y-3 p-4">
      <div className="flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
        <div>
          <div className="text-sm font-bold text-gray-800 dark:text-gray-100">{t("蜂窝 IMS 模组配置")}</div>
          <div className="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
            {t("这是模组级持久配置，会影响 VoLTE、IMS 短信和 IMS 语音；切换 SIM/Profile 不会自动改写。")}
          </div>
        </div>
        {status ? (
          <Tag type={status.volteCapable ? "success" : "info"}>
            {status.volteCapable ? t("VoLTE 可用") : t("VoLTE 未就绪")}
          </Tag>
        ) : null}
      </div>
      {!deviceOnline ? (
        <div className="text-xs text-amber-600 dark:text-amber-400">{t("设备离线，无法读取或修改模组 IMS 配置。")}</div>
      ) : null}
      {loadError ? <div role="alert" className="break-words text-xs text-red-600 dark:text-red-400">{loadError}</div> : null}
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <Select
          className="min-w-0 flex-1"
          value={selectedMode}
          disabled={!deviceOnline || loading || applying}
          onChange={(value) => setSelectedMode(value as CellularIMSMode)}
          options={MODE_OPTIONS.map((option) => ({ ...option, label: t(option.label) }))}
        />
        <Button
          variant={selectedMode === "force_disabled" ? "danger" : "primary"}
          loading={applying || loading}
          disabled={!deviceOnline || loading || applying || !status || selectedMode === status.mode}
          onClick={() => void apply()}
        >
          {t("应用并重启")}
        </Button>
      </div>
    </div>
  );
}
