import { AddRegular, DeleteRegular } from "@fluentui/react-icons";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input, Textarea } from "../ui/Input";
import { Select } from "../ui/Select";
import { Switch } from "../ui/Switch";
import { ChannelHeader, EmptyLine, Field, UrlListEditor } from "./controls";
import { HEADER_NAME_SUGGESTIONS, nextHeaderRowId } from "./model";
import type { BarkForm, EmailForm, HeaderRow, LarkForm, WebhookForm, WecomForm } from "./model";

const HEADER_LIST_ID = "vocat-webhook-header-names";

interface PushChannelProps<T> {
  value: T;
  onChange: (patch: Partial<T>) => void;
  testing: boolean;
  onTest: () => void;
}

function hasAnyUrl(urls: string[]): boolean {
  return Array.isArray(urls) && urls.some((url) => String(url || "").trim().length > 0);
}

function OneWayNotificationHint() {
  const { t } = useI18n();
  return (
    <div className="mb-4 rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
      {t("该渠道仅用于单向通知，不提供设备控制功能。新短信会逐条推送；启用通知的自动任务也会推送执行结果。")}
    </div>
  );
}

const BARK_LEVEL_OPTIONS = [
  { value: "timeSensitive", label: "Time-Sensitive (timeSensitive)" },
  { value: "active", label: "Active (active)" },
  { value: "passive", label: "Passive (passive)" },
];

export function BarkTab({ value, onChange, testing, onTest }: PushChannelProps<BarkForm>) {
  const { t } = useI18n();
  const off = !value.enabled;
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用 Bark 推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !hasAnyUrl(value.urls)} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <UrlListEditor
          urls={value.urls}
          onChange={(urls) => onChange({ urls })}
          enabled={value.enabled}
          placeholder="https://api.day.app/YOUR_KEY/"
          emptyText={t("尚未配置任何 Bark URL，点击右侧添加按钮。")}
        />
        <Field label={t("分组 (Group)")} hint={t("iOS 设备上的通知分组。")}>
          <Input value={value.group} onChange={(e) => onChange({ group: e.target.value })} disabled={off} placeholder={t("例如 vocat")} />
        </Field>
        <Field label={t("通知级别 (Level)")} hint={t("iOS 的专注模式/打扰规则会根据此级别决定是否亮屏。")}>
          <Select
            value={value.level}
            onChange={(level) => onChange({ level })}
            options={BARK_LEVEL_OPTIONS}
            disabled={off}
            placeholder={t("选择通知级别")}
            className="w-full"
          />
        </Field>
        <Field label={t("图标 (Icon)")}>
          <Input value={value.icon} onChange={(e) => onChange({ icon: e.target.value })} disabled={off} placeholder={t("图标 URL，可选")} />
        </Field>
      </div>
    </div>
  );
}

export function EmailTab({ value, onChange, testing, onTest }: PushChannelProps<EmailForm>) {
  const { t } = useI18n();
  const off = !value.enabled;
  const complete = !!(value.smtpHost && value.smtpPort && value.username && value.password && value.fromAddress && value.toAddresses);
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用 Email 推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !complete} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-10">
          <Field label={t("SMTP 主机")} className="sm:col-span-5">
            <Input value={value.smtpHost} onChange={(e) => onChange({ smtpHost: e.target.value })} disabled={off} placeholder="smtp.example.com" />
          </Field>
          <Field label={t("SMTP 端口")} className="sm:col-span-3">
            <Input
              value={value.smtpPort}
              onChange={(e) => {
                const smtpPort = e.target.value;
                onChange(Number(smtpPort) === 465 ? { smtpPort, useSsl: true } : { smtpPort });
              }}
              disabled={off}
              type="number"
              inputMode="numeric"
              placeholder="465 / 587"
            />
          </Field>
          <div className="space-y-1 sm:col-span-2">
            <label className="block text-xs font-bold uppercase tracking-wider text-gray-500">{t("使用 SSL/TLS")}</label>
            <div className="flex h-10 items-center">
              <Switch checked={value.useSsl} onChange={(useSsl) => onChange({ useSsl })} disabled={off} />
            </div>
          </div>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t("用户名 (Username)")}>
            <Input value={value.username} onChange={(e) => onChange({ username: e.target.value })} disabled={off} placeholder={t("邮箱账号")} />
          </Field>
          <Field label={t("密码 (Password)")}>
            <Input value={value.password} onChange={(e) => onChange({ password: e.target.value })} disabled={off} type="password" placeholder={t("邮箱密码或授权码")} />
          </Field>
        </div>
        <Field label={t("发件人地址 (From)")}>
          <Input value={value.fromAddress} onChange={(e) => onChange({ fromAddress: e.target.value })} disabled={off} placeholder={t("例如 noreply@example.com")} />
        </Field>
        <Field label={t("收件人地址 (To)")}>
          <Input value={value.toAddresses} onChange={(e) => onChange({ toAddresses: e.target.value })} disabled={off} placeholder={t("多个收件人请用英文逗号分隔")} />
        </Field>
      </div>
    </div>
  );
}

export function WebhookTab({ value, onChange, testing, onTest }: PushChannelProps<WebhookForm>) {
  const { t, lang } = useI18n();
  const off = !value.enabled;
  const rows = value.headers || [];
  const setRows = (headers: HeaderRow[]) => onChange({ headers });
  const addRow = () => setRows([...rows, { id: nextHeaderRowId(), key: "", value: "" }]);
  const removeRow = (index: number) => setRows(rows.filter((_, i) => i !== index));
  const updateRow = (index: number, patch: Partial<HeaderRow>) =>
    setRows(rows.map((row, i) => (i === index ? { ...row, ...patch } : row)));
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用 Webhook 推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !hasAnyUrl(value.urls)} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <UrlListEditor
          urls={value.urls}
          onChange={(urls) => onChange({ urls })}
          enabled={value.enabled}
          placeholder="https://..."
          emptyText={t("尚未配置任何 Webhook URL，点击右侧添加按钮。")}
        />
        <Field label={t("数字签名密钥 (Secret)")} hint={t("若配置，将通过请求头 X-Vocat-Signature 提供 payload 验证。")}>
          <Input value={value.secret} onChange={(e) => onChange({ secret: e.target.value })} disabled={off} placeholder={t("用于 HMAC-SHA256 签名，选填")} />
        </Field>
        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-xs font-bold uppercase tracking-wider text-gray-500">{t("自定义请求头 (Headers)")}</label>
            <Button size="small" variant="primary" plain onClick={addRow} disabled={off} icon={<AddRegular />}>
              {t("添加 Header")}
            </Button>
          </div>
          {rows.length === 0 ? <EmptyLine>{t("尚未配置自定义请求头，例如 Authorization、X-Api-Key 等。")}</EmptyLine> : null}
          {rows.map((row, index) => (
            <div key={row.id} className="flex items-center gap-2">
              <Input
                list={HEADER_LIST_ID}
                value={row.key}
                onChange={(e) => updateRow(index, { key: e.target.value })}
                disabled={off}
                placeholder={t("选择或输入 Header 名")}
                className="flex-1"
              />
              <Input
                value={row.value}
                onChange={(e) => updateRow(index, { value: e.target.value })}
                disabled={off}
                placeholder={t("值，如 Bearer xxx")}
                className="flex-1"
              />
              <Button variant="danger" plain onClick={() => removeRow(index)} disabled={off} aria-label={t("删除")} icon={<DeleteRegular />} />
            </div>
          ))}
          <datalist id={HEADER_LIST_ID}>
            {HEADER_NAME_SUGGESTIONS.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>
          <div className="mt-1 text-[10px] text-gray-400">{t("Content-Type 与 X-Vocat-Signature 为系统保留头，自定义同名头会被忽略。")}</div>
        </div>
        <Field
          label={t("文本模板 (Text Template)")}
          hint={
            lang === "zh" ? (
              <>
                支持占位符：<code>{"{{text}}"}</code>、<code>{"{{event}}"}</code>、<code>{"{{timestamp}}"}</code>、<code>{"{{device_id}}"}</code>、
                <code>{"{{device_name}}"}</code>、<code>{"{{device_label}}"}</code>、<code>{"{{number}}"}</code>、<code>{"{{time}}"}</code>。留空则使用标准短信模板。
              </>
            ) : (
              <>
                Supported placeholders: <code>{"{{text}}"}</code>, <code>{"{{event}}"}</code>, <code>{"{{timestamp}}"}</code>,{" "}
                <code>{"{{device_id}}"}</code>, <code>{"{{device_name}}"}</code>, <code>{"{{device_label}}"}</code>, <code>{"{{number}}"}</code>, and
                <code>{"{{time}}"}</code>. Leave empty to use the standard SMS template.
              </>
            )
          }
        >
          <Textarea
            value={value.textTemplate}
            onChange={(e) => onChange({ textTemplate: e.target.value })}
            disabled={off}
            rows={2}
            placeholder={"收到新短信\n设备  {{device_label}}\n号码  {{number}}\n时间  {{time}}\n内容  {{text}}"}
          />
        </Field>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field label={t("请求超时 (ms)")}>
            <Input
              value={value.timeoutMs}
              onChange={(e) => {
                const n = e.target.valueAsNumber;
                onChange({ timeoutMs: Number.isNaN(n) ? 0 : n });
              }}
              disabled={off}
              type="number"
              min={1000}
              max={60000}
            />
          </Field>
          <Field label={t("最大重试次数")}>
            <Input
              value={value.retryMax}
              onChange={(e) => {
                const n = e.target.valueAsNumber;
                onChange({ retryMax: Number.isNaN(n) ? 0 : n });
              }}
              disabled={off}
              type="number"
              min={0}
              max={10}
            />
          </Field>
        </div>
      </div>
    </div>
  );
}

export function WecomTab({ value, onChange, testing, onTest }: PushChannelProps<WecomForm>) {
  const { t, lang } = useI18n();
  const off = !value.enabled;
  const complete = hasAnyUrl(value.urls) && !!value.payloadTemplate.trim();
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用企业微信消息推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !complete} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <div className="rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
          {t("每个企业微信消息推送 Webhook URL 单独占一行，点击添加 URL 新增一行；不使用逗号、空格或换行分隔多个 URL。")}
        </div>
        <UrlListEditor
          urls={value.urls}
          onChange={(urls) => onChange({ urls })}
          enabled={value.enabled}
          placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
          emptyText={t("尚未配置任何企业微信消息推送 Webhook URL，点击右侧添加按钮。")}
        />
        <Field
          label={t("JSON 请求体模板")}
          hint={
            <>
              {t("支持完整企业微信消息推送 JSON。变量必须作为 JSON 值使用，例如")} <code>{"{{message}}"}</code>{lang === "zh" ? "。" : ". "}
              {t("可用变量：{{event}}、{{title}}、{{message}}、{{timestamp}}、{{content}}、{{number}}、{{device_id}}、{{device_name}}、{{device_label}}、{{time}}。")}
            </>
          }
        >
          <Textarea
            value={value.payloadTemplate}
            onChange={(event) => onChange({ payloadTemplate: event.target.value })}
            disabled={off}
            rows={12}
            className="font-mono text-xs"
          />
        </Field>
      </div>
    </div>
  );
}

export function LarkTab({ value, onChange, testing, onTest }: PushChannelProps<LarkForm>) {
  const { t, lang } = useI18n();
  const off = !value.enabled;
  const complete = !!value.url.trim() && !!value.payloadTemplate.trim() && (!value.signingEnabled || !!value.secret.trim());
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用飞书 / Lark 群自定义机器人通知")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !complete} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <div className="rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
          {t("支持飞书 open.feishu.cn 与国际版 Lark open.larksuite.com 的群自定义机器人 Webhook，无需创建应用。")}
        </div>
        <Field label={t("群机器人 Webhook URL")}>
          <Input
            value={value.url}
            onChange={(event) => {
              const url = event.target.value;
              onChange(value.url === "********" && url !== value.url ? { url, secret: "" } : { url });
            }}
            disabled={off}
            placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..."
          />
        </Field>
        <div className="space-y-1">
          <label className="block text-xs font-bold uppercase tracking-wider text-gray-500">{t("启用签名校验")}</label>
          <div className="flex h-10 items-center">
            <Switch checked={value.signingEnabled} onChange={(signingEnabled) => onChange({ signingEnabled })} disabled={off} />
          </div>
        </div>
        {value.signingEnabled ? (
          <Field
            label={t("签名密钥 (Secret)")}
            hint={t("填写群机器人安全设置生成的签名密钥；Webhook URL 与密钥都会作为敏感配置并在页面中脱敏。")}
          >
            <Input
              value={value.secret}
              onChange={(event) => onChange({ secret: event.target.value })}
              disabled={off}
              type="password"
              placeholder={t("群机器人签名密钥")}
            />
          </Field>
        ) : null}
        <Field
          label={t("JSON 请求体模板")}
          hint={
            <>
              {t("支持完整飞书 / Lark 群自定义机器人 JSON。变量必须作为 JSON 值使用，例如")} <code>{"{{message}}"}</code>{lang === "zh" ? "。" : ". "}
              {t("可用变量：{{event}}、{{title}}、{{message}}、{{timestamp}}、{{content}}、{{number}}、{{device_id}}、{{device_name}}、{{device_label}}、{{time}}。")}
            </>
          }
        >
          <Textarea
            value={value.payloadTemplate}
            onChange={(event) => onChange({ payloadTemplate: event.target.value })}
            disabled={off}
            rows={12}
            className="font-mono text-xs"
          />
        </Field>
      </div>
    </div>
  );
}
