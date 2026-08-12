import { SendRegular } from "@fluentui/react-icons";
import type { DeveloperSettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { CardDecor, CardIcon, CardTitle } from "./Cards";

export function SMSRateLimitCard({
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
          <SendRegular className="text-[24px]" />
        </CardIcon>
        <CardTitle
          title={zh ? "短信发送速率限制" : "SMS send rate limit"}
          subtitle={zh ? "所有设备与 SIM 卡共享的全局发送额度" : "One global quota shared by every device and SIM"}
        />
      </div>
      <div className="relative z-10 space-y-4">
        <Input
          type="number"
          min={1}
          max={value?.maxSmsHourlyLimit ?? 1000}
          value={Number.isFinite(limit) ? limit : ""}
          disabled={loading || saving}
          onChange={(event) => onLimitChange(Number(event.target.value))}
          suffix={zh ? "条 / 小时" : "messages / hour"}
        />
        <p className="text-xs leading-5 text-gray-500 dark:text-gray-400">
          {zh
            ? `采用滚动一小时窗口，网页、TG Bot、自动任务、API、VoWiFi 与基站发送全部计入；接收短信不受限制。关闭开发者模式后恢复为 ${value?.defaultSmsHourlyLimit ?? 10} 条/小时。`
            : `Uses a rolling one-hour window across the web UI, Telegram bot, automatic tasks, API, VoWiFi, and cellular sending. Receiving is unlimited. Disabling developer mode restores ${value?.defaultSmsHourlyLimit ?? 10} messages/hour.`}
        </p>
        <Button variant="primary" loading={saving} disabled={loading} onClick={onSave} className="w-full !border-0">
          {zh ? "保存短信速率限制" : "Save SMS rate limit"}
        </Button>
      </div>
    </div>
  );
}
