import { useI18n } from "../../lib/i18n";
import { Input } from "../ui/Input";
import { Select } from "../ui/Select";
import { ChannelHeader, Field } from "./controls";
import type { PushplusForm, TelegramForm } from "./model";

interface ChannelProps<T> {
  value: T;
  onChange: (patch: Partial<T>) => void;
}

export function TelegramTab({ value, onChange }: ChannelProps<TelegramForm>) {
  const { t } = useI18n();
  const off = !value.enabled;
  return (
    <div className="pt-2">
      <ChannelHeader title={t("启用 Telegram 机器人")} enabled={value.enabled} onToggle={(enabled) => onChange({ enabled })} />
      <div className="space-y-4">
        <div className="rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
          {t("启用后会推送新短信，并允许指定管理员通过 Bot 查看状态、切卡、管理 WiFi Calling、发送短信和限时拨号。拨号只执行呼叫并自动挂断，不处理音频。")}
        </div>
        <Field label="Bot Token">
          <Input value={value.botToken} onChange={(e) => onChange({ botToken: e.target.value })} disabled={off} placeholder="xxxx:yyyy" />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label="Chat ID" hint={t("接收短信通知和命令回复的私聊或群组 ID。群组 ID 可以是负数。")}>
            <Input value={value.chatId} onChange={(e) => onChange({ chatId: e.target.value })} disabled={off} type="number" inputMode="numeric" placeholder={t("例如 123456")} />
          </Field>
          <Field label="Admin ID" hint={t("只有该 Telegram 用户可以执行控制命令；留空时仅推送通知，不接受命令。")}>
            <Input value={value.adminId} onChange={(e) => onChange({ adminId: e.target.value })} disabled={off} type="number" inputMode="numeric" placeholder={t("例如 123456")} />
          </Field>
        </div>
        <Field label={t("TG API 反代（可选）")} hint={t("反向代理地址 (例如 https://api.telegram.org/bot%s/%s)")}>
          <Input value={value.baseUrl} onChange={(e) => onChange({ baseUrl: e.target.value })} disabled={off} placeholder={t("留空直连 api.telegram.org；需要反代时填写")} />
        </Field>
        <Field label={t("HTTP 代理（可选）")} hint={t("用于连接 API 服务器的 HTTP 代理")}>
          <Input value={value.proxy} onChange={(e) => onChange({ proxy: e.target.value })} disabled={off} placeholder={t("例如 http://127.0.0.1:7890")} />
        </Field>
      </div>
    </div>
  );
}

const PUSHPLUS_CHANNEL_OPTIONS = [
  { value: "wechat", label: "WeChat (wechat)" },
  { value: "webhook", label: "Webhook (webhook)" },
  { value: "cp", label: "WeCom (cp)" },
  { value: "mail", label: "Email (mail)" },
];

export function PushplusTab({ value, onChange }: ChannelProps<PushplusForm>) {
  const { t } = useI18n();
  const off = !value.enabled;
  return (
    <div className="pt-2">
      <ChannelHeader title={t("启用 Pushplus 推送")} enabled={value.enabled} onToggle={(enabled) => onChange({ enabled })} />
      <div className="space-y-4">
        <div className="rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
          {t("该渠道仅用于单向通知，不提供设备控制功能。新短信会逐条推送；启用通知的自动任务也会推送执行结果。")}
        </div>
        <Field label="Token">
          <Input value={value.token} onChange={(e) => onChange({ token: e.target.value })} disabled={off} placeholder={t("Pushplus 用户的 Token")} />
        </Field>
        <Field label={t("群组编码 (Topic)")}>
          <Input value={value.topic} onChange={(e) => onChange({ topic: e.target.value })} disabled={off} placeholder={t("群组编码，不填则发给个人")} />
        </Field>
        <Field label={t("渠道 (Channel)")}>
          <Select
            value={value.channel}
            onChange={(channel) => onChange({ channel })}
            options={PUSHPLUS_CHANNEL_OPTIONS}
            disabled={off}
            placeholder={t("选择渠道")}
            className="w-full"
          />
        </Field>
      </div>
    </div>
  );
}
