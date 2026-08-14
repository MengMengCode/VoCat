import { SettingsRegular } from "@fluentui/react-icons";
import type { DeveloperSettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { CardDecor, CardIcon, CardTitle } from "./Cards";

export function DeviceQuotaCard({
  value,
  limit,
  loading,
  saving,
  onLimitChange,
  onSave,
}: {
  value: DeveloperSettings | null;
  limit: number;
  loading: boolean;
  saving: boolean;
  onLimitChange: (limit: number) => void;
  onSave: () => void;
}) {
  const { lang } = useI18n();
  const zh = lang === "zh";
  return (
    <div className="ui-card group relative overflow-hidden p-8">
      <CardDecor />
      <div className="relative z-10 mb-6 flex items-center gap-3">
        <CardIcon>
          <SettingsRegular className="text-[24px]" />
        </CardIcon>
        <CardTitle
          title={zh ? "设备配额" : "Device quota"}
          subtitle={zh ? "最多允许配置的设备数量" : "Maximum number of configurable devices"}
        />
      </div>
      <div className="relative z-10 space-y-4">
        <Input
          type="number"
          min={1}
          max={value?.maxDeviceLimit ?? 10}
          value={Number.isFinite(limit) ? limit : ""}
          disabled={loading || saving}
          onChange={(event) => onLimitChange(Number(event.target.value))}
          suffix={zh ? "台" : "devices"}
        />
        <p className="text-xs text-gray-500 dark:text-gray-400">
          {zh
            ? `恢复默认配置后会自动恢复为 ${value?.defaultDeviceLimit ?? 5} 台，不会删除已经添加的设备。`
            : `Restoring the default configuration resets the quota to ${value?.defaultDeviceLimit ?? 5}; existing devices are not deleted.`}
        </p>
        <Button variant="primary" loading={saving} disabled={loading} onClick={onSave} className="w-full !border-0">
          {zh ? "保存设备配额" : "Save device quota"}
        </Button>
      </div>
    </div>
  );
}
