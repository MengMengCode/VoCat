import { useEffect, useMemo, useRef, useState } from "react";
import { renderSVG } from "uqr";
import { AddRegular, DeleteRegular } from "@fluentui/react-icons";
import { useI18n } from "../../lib/i18n";
import { Button } from "../ui/Button";
import { Input, Textarea } from "../ui/Input";
import { Select } from "../ui/Select";
import { Switch } from "../ui/Switch";
import type { DeviceListItem } from "../../types";
import { ChannelHeader, EmptyLine, Field, NotificationDeviceScope, SegmentedTabs, UrlListEditor } from "./controls";
import {
  HEADER_NAME_SUGGESTIONS,
  nextHeaderRowId,
  qqBotBindActive,
  qqBotBindRemaining,
  qqBotConfigured,
  qqBotDefaultMethod,
  qqBotNextView,
  qqBotVisibleMethod,
} from "./model";
import type {
  BarkForm,
  EmailForm,
  HeaderRow,
  LarkForm,
  NotifyForms,
  QQBotBindMethod,
  QQBotBindMode,
  QQBotBindState,
  QQBotForm,
  QQBotView,
  WebhookForm,
  WecomForm,
} from "./model";

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

// Show a Bark URL with its device key shortened but still telling URLs apart:
// "https://api.day.app/AbCdEfGh1234/" -> "api.day.app/AbCd…1234".
function shortBarkUrl(url: string): string {
  try {
    const parsed = new URL(url);
    const key = parsed.pathname.split("/").filter(Boolean)[0] ?? "";
    return `${parsed.host}/${key.length > 10 ? `${key.slice(0, 4)}…${key.slice(-4)}` : key}`;
  } catch {
    return url;
  }
}

export function BarkTab({ value, onChange, testing, onTest, devices, devicesFailed }: PushChannelProps<BarkForm> & {
  devices: Pick<DeviceListItem, "id" | "name">[] | null;
  devicesFailed: boolean;
}) {
  const { t } = useI18n();
  const off = !value.enabled;
  const urls = [...new Set((value.urls || []).map((url) => url.trim()).filter(Boolean))];
  const scopeOf = (url: string) => (value.urlScopes || []).find((scope) => scope.url.trim() === url)?.deviceIds ?? null;
  const setScope = (url: string, deviceIds: string[] | null) => {
    const others = (value.urlScopes || []).filter((scope) => scope.url.trim() !== url);
    onChange({ urlScopes: deviceIds === null ? others : [...others, { url, deviceIds }] });
  };
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
        {/* Also shown for a single URL that still has a scope, so it can be cleared after removing the others. */}
        {urls.length > 1 || urls.some((url) => scopeOf(url) !== null) ? (
          <Field label={t("按链接指定设备")} hint={t("每个链接可以只接收部分设备的通知，例如一个链接只收读卡器 A，另一个只收读卡器 B。未开启的链接按下方「仅推送指定设备」推送。")}>
            <div className="space-y-2">
              {urls.map((url) => (
                <NotificationDeviceScope key={url} value={scopeOf(url)} onChange={(ids) => setScope(url, ids)}
                  devices={devices} failed={devicesFailed} title={shortBarkUrl(url)} hint={t("开启后，这个链接只推送所选设备的通知。")}
                  className="space-y-3 rounded-lg border border-gray-200 p-3 text-sm dark:border-white/10" />
              ))}
            </div>
          </Field>
        ) : null}
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

export function EmailTab({ value, onChange, testing, onTest, onClear }: PushChannelProps<EmailForm> & { onClear: () => void }) {
  const { t } = useI18n();
  const off = !value.enabled;
  const complete = !!(value.smtpHost && value.smtpPort && value.username && value.password && value.fromAddress && value.toAddresses);
  return (
    <div className="pt-2">
      <ChannelHeader
        onClear={onClear}
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
                <code>{"{{device_name}}"}</code>、<code>{"{{device_label}}"}</code>、<code>{"{{number}}"}</code>、<code>{"{{local_phone}}"}</code>、
                <code>{"{{iccid}}"}</code>、<code>{"{{recipient}}"}</code>、<code>{"{{time}}"}</code>。
                number 为发送方；local_phone 为本机号码；recipient 为本机号码或 ICCID，无信息时为“未知”。留空则使用标准短信模板。
              </>
            ) : (
              <>
                Supported placeholders: <code>{"{{text}}"}</code>, <code>{"{{event}}"}</code>, <code>{"{{timestamp}}"}</code>,{" "}
                <code>{"{{device_id}}"}</code>, <code>{"{{device_name}}"}</code>, <code>{"{{device_label}}"}</code>, <code>{"{{number}}"}</code>,{" "}
                <code>{"{{local_phone}}"}</code>, <code>{"{{iccid}}"}</code>, <code>{"{{recipient}}"}</code>, and <code>{"{{time}}"}</code>.
                number is the sender; local_phone is the local number; recipient uses the local number, ICCID, or “未知” if unavailable. Leave empty to use the standard SMS template.
              </>
            )
          }
        >
          <Textarea
            value={value.textTemplate}
            onChange={(e) => onChange({ textTemplate: e.target.value })}
            disabled={off}
            rows={2}
            placeholder={"收到新短信\n设备  {{device_label}}\n收信卡  {{recipient}}\n号码  {{number}}\n时间  {{time}}\n内容  {{text}}"}
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
              {t("可用变量：{{event}}、{{title}}、{{message}}、{{timestamp}}、{{content}}、{{number}}、{{local_phone}}、{{iccid}}、{{recipient}}、{{device_id}}、{{device_name}}、{{device_label}}、{{time}}。")}
              {t("number 为发送方；local_phone 为本机号码；recipient 为本机号码或 ICCID，无信息时为“未知”。")}
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
              {t("可用变量：{{event}}、{{title}}、{{message}}、{{timestamp}}、{{content}}、{{number}}、{{local_phone}}、{{iccid}}、{{recipient}}、{{device_id}}、{{device_name}}、{{device_label}}、{{time}}。")}
              {t("number 为发送方；local_phone 为本机号码；recipient 为本机号码或 ICCID，无信息时为“未知”。")}
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

export function MeowTab({ value, onChange, testing, onTest }: PushChannelProps<NotifyForms["meow"]>) {
  const { t } = useI18n();
  const off = !value.enabled;
  return (
    <div className="pt-2">
      <ChannelHeader
        title={t("启用 MeoW 推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={off || !value.nickname.trim()} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="space-y-4">
        <Field label={t("MeoW 昵称")}>
          <Input aria-label={t("MeoW 昵称")} value={value.nickname} onChange={(e) => onChange({ nickname: e.target.value })} disabled={off} />
        </Field>
        <Field label={t("跳转链接 (url)")}>
          <Input aria-label={t("跳转链接 (url)")} value={value.url} onChange={(e) => onChange({ url: e.target.value })} disabled={off} placeholder={t("点击通知时打开的链接，选填。")} />
        </Field>
        <Field label={t("通知图标 (imgUrl)")}>
          <Input aria-label={t("通知图标 (imgUrl)")} value={value.imgUrl} onChange={(e) => onChange({ imgUrl: e.target.value })} disabled={off} placeholder={t("图标 URL，建议使用 216×216 PNG，选填。")} />
        </Field>
      </div>
    </div>
  );
}

/** QQ 机器人渠道：未绑定时显示扫码和手动两种绑定流程，绑定完成后显示可编辑的维护面板。 */
export function QQBotTab({
  value,
  onChange,
  testing,
  onTest,
  onClear,
  bind,
  binding,
  syncing,
  onBind,
  onCancelBind,
}: PushChannelProps<QQBotForm> & {
  onClear: () => void;
  bind: QQBotBindState | null;
  binding: boolean;
  /** 正在从服务器同步绑定结果：期间锁定凭据和接收方，避免编辑被同步结果覆盖。 */
  syncing: boolean;
  onBind: (mode: QQBotBindMode) => void;
  onCancelBind: () => void;
}) {
  const { t } = useI18n();
  const hasCredentials = !!value.appId.trim() && !!value.appSecret;
  const configured = qqBotConfigured(value);
  const active = qqBotBindActive(bind);
  // 绑定完成后显示维护面板；清空配置后回到绑定流程。编辑维护面板中的单个字段不跳转。
  const [view, setView] = useState<QQBotView>(() => qqBotNextView("bind", value));
  useEffect(() => {
    setView((previous) => qqBotNextView(previous, value));
  }, [value.appId, value.appSecret, value.targetOpenid]);
  const maintaining = view === "maintain" && !active;
  // 选择的绑定方式只是页面状态，不保存。
  const [selected, setSelected] = useState<QQBotBindMethod>(() => qqBotDefaultMethod(value));
  const method = qqBotVisibleMethod(selected, bind);
  // 每次显示扫码面板（进入未绑定的页面、从手动绑定切换过来、清空配置后）自动生成一次二维码；
  // 取消或过期后不自动重试，由“重新生成”按钮处理。
  const scanVisible = !maintaining && method === "qr";
  const autoStarted = useRef(false);
  useEffect(() => {
    if (!scanVisible) {
      autoStarted.current = false;
      return;
    }
    if (active) {
      autoStarted.current = true;
      return;
    }
    if (autoStarted.current || configured || binding || syncing) return;
    autoStarted.current = true;
    onBind("qr");
  }, [scanVisible, active, configured, binding, syncing]);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  // 扫码回退到绑定码时，结束后仍停留在手动方式。
  const previousMode = useRef<QQBotBindMode | null>(null);
  const [fellBack, setFellBack] = useState(false);
  useEffect(() => {
    if (!active || !bind) return;
    setSelected(bind.mode === "qr" ? "qr" : "manual");
    // 同一次绑定从扫码变为绑定码，说明平台没有返回扫码用户。
    setFellBack(previousMode.current === "qr" && bind.mode === "code");
    previousMode.current = bind.mode;
  }, [active, bind?.mode]);
  useEffect(() => {
    // 绑定结束后清除回退状态，下一次绑定不会沿用上一次的提示和隐藏的凭据字段。
    if (!active) {
      previousMode.current = null;
      setFellBack(false);
    }
  }, [active]);
  const qrUrl = active && bind?.mode === "qr" ? bind.qrUrl : undefined;
  // 二维码只编码开放平台的绑定页地址，在本地生成，不经第三方服务。
  const qrImage = useMemo(
    () => (qrUrl ? `data:image/svg+xml;utf8,${encodeURIComponent(renderSVG(qrUrl, { pixelSize: 6 }))}` : ""),
    [qrUrl],
  );
  const remaining = bind ? `${t("剩余")} ${qqBotBindRemaining(bind.expiresAt, now)}` : "";
  // 结束的绑定只在对应方式的面板中提示。
  const ended = !active && bind && (bind.status === "failed" || bind.status === "expired") ? bind : null;
  const endedIn = (mode: QQBotBindMode) =>
    ended && ended.mode === mode ? (
      <div className="text-xs text-red-600 dark:text-red-400">
        {ended.status === "expired" ? t("绑定已过期，请重新绑定。") : t("绑定失败")}
        {ended.error ? ` · ${ended.error}` : ""}
      </div>
    ) : null;
  const bindError = active && bind?.error ? <div className="text-xs text-red-600 dark:text-red-400">{bind.error}</div> : null;
  const panel = "space-y-3 rounded-lg border border-gray-200 px-4 py-4 dark:border-gray-700";
  const step = "flex items-center gap-2 text-sm font-semibold text-gray-800 dark:text-gray-100";
  const stepNumber = "flex h-5 w-5 items-center justify-center rounded-full bg-[#0ea5e9]/10 text-xs text-[#0ea5e9] dark:text-[#7dd3fc]";
  return (
    <fieldset disabled={binding || syncing} className="min-w-0 pt-2">
      <ChannelHeader
        // 只有绑定完成后才有可清空的配置；清空后回到绑定流程。
        onClear={maintaining ? onClear : undefined}
        clearLabel={t("清空配置重新绑定")}
        title={t("启用 QQ 机器人推送")}
        enabled={value.enabled}
        onToggle={(enabled) => onChange({ enabled })}
        actions={
          <Button size="small" variant="primary" plain loading={testing} disabled={!value.enabled || !qqBotConfigured(value)} onClick={onTest}>
            {t("测试通知")}
          </Button>
        }
      />
      <OneWayNotificationHint />
      <div className="mb-4 rounded-lg bg-gray-50 px-3 py-2 text-xs leading-5 text-gray-500 dark:bg-gray-800/60 dark:text-gray-400">
        {t("通过 QQ 开放平台官方机器人私聊推送。QQ 限制机器人主动消息，被拒绝时会在测试结果和日志中显示。")}
      </div>

      {maintaining ? (
        <div className="space-y-4">
          <div className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <Field label={t("机器人 AppID")}>
                <Input aria-label={t("机器人 AppID")} value={value.appId} onChange={(e) => onChange({ appId: e.target.value })} disabled={syncing} inputMode="numeric" />
              </Field>
              <Field label={t("机器人 AppSecret")}>
                <Input aria-label={t("机器人 AppSecret")} value={value.appSecret} onChange={(e) => onChange({ appSecret: e.target.value })} disabled={syncing} type="password" />
              </Field>
            </div>
            <Field label={t("接收方 openid（QQ 单聊用户）")}>
              <Input
                aria-label={t("接收方 openid（QQ 单聊用户）")}
                value={value.targetOpenid}
                onChange={(e) => onChange({ targetOpenid: e.target.value })}
                disabled={syncing}
                className="font-mono"
              />
            </Field>
          </div>
        </div>
      ) : (
        <>
          <div className="mb-3 text-sm font-semibold text-gray-800 dark:text-gray-100">{t("绑定 QQ 机器人")}</div>
          <SegmentedTabs
            tabs={[
              { key: "qr", label: t("扫码绑定") },
              { key: "manual", label: t("手动绑定") },
            ]}
            value={method}
            onChange={(key) => {
              // 二维码已生成时可以改用手动绑定：放弃当前扫码任务，切回扫码页时再生成新的二维码。
              if (key === "manual" && active && bind?.mode === "qr") onCancelBind();
              setSelected(key as QQBotBindMethod);
            }}
            // 等待绑定码期间仍锁定，避免丢失已显示的绑定码。
            disabled={active && bind?.mode === "code"}
          />

          {method === "qr" ? (
            <div className={`-mt-2 ${panel}`}>
              {active && bind?.mode === "qr" ? (
                <>
                  <div className="flex flex-wrap items-center gap-3 text-xs text-gray-500">
                    <span>{t("等待扫码")} · {remaining}</span>
                    <Button size="small" plain onClick={onCancelBind}>{t("取消")}</Button>
                  </div>
                  {qrImage ? <img src={qrImage} alt={t("QQ 机器人绑定二维码")} className="h-44 w-44 rounded bg-white p-1" /> : null}
                  <div className="text-xs leading-5 text-gray-600 dark:text-gray-300">
                    {t("用手机 QQ 扫描二维码，按提示选择或创建机器人并确认。VoCat 会自动保存机器人凭据，并把扫码的 QQ 账号设为接收方。")}
                  </div>
                  {bindError}
                </>
              ) : (
                <>
                  {endedIn("qr")}
                  <Button size="small" variant="primary" loading={binding} onClick={() => onBind("qr")}>
                    {ended?.mode === "qr" ? t("重新生成") : t("生成二维码")}
                  </Button>
                </>
              )}
            </div>
          ) : (
            <div className={`-mt-2 ${panel}`}>
              {/* 扫码回退时凭据来自扫码结果，只保存在本次绑定中，不显示手动填写的步骤。 */}
              {fellBack && active ? null : (
                <>
                  <div className={step}>
                    <span className={stepNumber}>1</span>
                    {t("填写机器人凭据")}
                  </div>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    <Field label={t("机器人 AppID")}>
                      <Input aria-label={t("机器人 AppID")} value={value.appId} onChange={(e) => onChange({ appId: e.target.value })} disabled={active || syncing} inputMode="numeric" />
                    </Field>
                    <Field label={t("机器人 AppSecret")}>
                      <Input aria-label={t("机器人 AppSecret")} value={value.appSecret} onChange={(e) => onChange({ appSecret: e.target.value })} disabled={active || syncing} type="password" />
                    </Field>
                  </div>

                  <div className={`${step} pt-2`}>
                    <span className={stepNumber}>2</span>
                    {t("绑定接收方")}
                  </div>
                </>
              )}
              {active && bind?.mode === "code" ? (
                <div className="rounded-lg bg-gray-50 px-3 py-3 text-xs leading-5 text-gray-600 dark:bg-gray-800/60 dark:text-gray-300">
                  {fellBack ? (
                    <div className="mb-2 text-amber-700 dark:text-amber-300">
                      {t("扫码未返回接收方，请在 QQ 中私聊刚才选择的机器人，发送下方绑定码。完成后会一起保存机器人凭据。")}
                    </div>
                  ) : null}
                  <div className="mb-2 font-mono text-lg font-semibold tracking-wider text-gray-900 dark:text-gray-100">{bind.code}</div>
                  <div>{t("在 QQ 中私聊机器人，发送上面的绑定码。")}</div>
                  <div className="mt-1 flex flex-wrap items-center gap-3 text-gray-500">
                    <span>
                      {bind.status === "connecting" ? t("正在连接 QQ 开放平台…") : t("已连接，等待绑定码")} · {remaining}
                    </span>
                    <Button size="small" plain onClick={onCancelBind}>{t("取消")}</Button>
                  </div>
                  {bindError}
                </div>
              ) : (
                <>
                  {hasCredentials ? (
                    <div className="text-xs leading-5 text-gray-600 dark:text-gray-300">
                      {t("点击获取绑定码，然后在 QQ 中私聊机器人发送该绑定码，VoCat 会把发送者设为接收方。")}
                    </div>
                  ) : null}
                  {endedIn("code")}
                  <Button size="small" variant="primary" loading={binding} disabled={!hasCredentials} onClick={() => onBind("code")}>
                    {t("获取绑定码")}
                  </Button>
                </>
              )}
            </div>
          )}

        </>
      )}

      <div className="mt-6">
        <Field label={t("HTTP 代理（可选）")} hint={t("用于连接 API 服务器的 HTTP 代理")}>
          <Input value={value.proxy} onChange={(e) => onChange({ proxy: e.target.value })} disabled={active} placeholder={t("例如 http://127.0.0.1:7890")} />
        </Field>
      </div>
    </fieldset>
  );
}
