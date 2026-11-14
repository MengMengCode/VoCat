import type { NotificationSettings } from "../../types";

// ---- 页面专属表单类型（camelCase，对应参考里的各 channel 表单） ----

export interface TelegramForm {
  enabled: boolean;
  botToken: string;
  chatId: string;
  adminId: string;
  baseUrl: string;
  proxy: string;
}

export interface HeaderRow {
  id: number;
  key: string;
  value: string;
}

export interface WebhookForm {
  enabled: boolean;
  urls: string[];
  secret: string;
  timeoutMs: number;
  retryMax: number;
  textTemplate: string;
  headers: HeaderRow[];
}

// Limits one Bark URL to some devices. URLs without an entry follow the channel's device scope.
export interface BarkUrlScope {
  url: string;
  deviceIds: string[];
}

export interface BarkForm {
  enabled: boolean;
  urls: string[];
  urlScopes: BarkUrlScope[];
  group: string;
  icon: string;
  level: string;
}

export interface EmailForm {
  enabled: boolean;
  useSsl: boolean;
  smtpHost: string;
  smtpPort: string;
  username: string;
  password: string;
  fromAddress: string;
  toAddresses: string; // 逗号分隔
}

export interface PushplusForm {
	enabled: boolean;
	token: string;
	topic: string;
	channel: string;
}

export interface WecomForm {
	enabled: boolean;
	urls: string[];
	payloadTemplate: string;
}

export interface LarkForm {
	enabled: boolean;
	url: string;
	signingEnabled: boolean;
	secret: string;
	payloadTemplate: string;
}

export const DEFAULT_WECOM_PAYLOAD_TEMPLATE = `{
  "msgtype": "text",
  "text": {
    "content": {{message}}
  }
}`;

export const DEFAULT_LARK_PAYLOAD_TEMPLATE = `{
  "msg_type": "text",
  "content": {
    "text": {{message}}
  }
}`;

export type QQBotForm = Omit<NotificationSettings["qqbot"], "deviceIds">;

interface ChannelForms {
  meow: NotificationSettings["meow"];
  qqbot: QQBotForm;
  telegram: TelegramForm;
  webhook: WebhookForm;
  bark: BarkForm;
	email: EmailForm;
	pushplus: PushplusForm;
	wecom: WecomForm;
	lark: LarkForm;
}

export type NotifyForms = {
  [K in keyof ChannelForms]: ChannelForms[K] & { deviceIds: string[] | null };
};

// 系统保留头，自定义同名头会被忽略（品牌 vocat）
export const RESERVED_HEADERS = new Set(["content-type", "x-vocat-signature"]);

export const HEADER_NAME_SUGGESTIONS = [
  "Authorization",
  "X-Api-Key",
  "X-Auth-Token",
  "X-Webhook-Token",
  "X-Signature",
  "X-Request-Id",
  "Accept",
  "User-Agent",
];

let headerRowSeq = 0;

export function nextHeaderRowId() {
  headerRowSeq += 1;
  return headerRowSeq;
}

function str(value: unknown): string {
  return value === null || value === undefined ? "" : String(value);
}

function num(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

export function recordToHeaderRows(raw: unknown): HeaderRow[] {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return [];
  return Object.entries(raw as Record<string, unknown>).map(([key, value]) => ({
    id: nextHeaderRowId(),
    key,
    value: value === null || value === undefined ? "" : String(value),
  }));
}

export function headerRowsToRecord(rows: HeaderRow[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const row of rows) {
    const key = String(row.key || "").trim();
    if (!key || RESERVED_HEADERS.has(key.toLowerCase())) continue;
    out[key] = String(row.value ?? "");
  }
  return out;
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function joinList(value: unknown): string {
  return Array.isArray(value) ? value.map((item) => String(item)).join(",") : "";
}

function strList(value: unknown): string[] {
  return Array.isArray(value) ? value.map((item) => String(item)) : [];
}

export function defaultNotifyForms(): NotifyForms {
  return formsFromNotifications({});
}

export function formsFromNotifications(data: Partial<NotificationSettings>): NotifyForms {
  const meow = asRecord(data.meow);
  const qqbot = asRecord(data.qqbot);
  const telegram = asRecord(data.telegram);
  const webhook = asRecord(data.webhook);
  const bark = asRecord(data.bark);
	const email = asRecord(data.email);
	const pushplus = asRecord(data.pushplus);
	const wecom = asRecord(data.wecom);
	const lark = asRecord(data.lark);
  const forms = {
    meow: { enabled: !!meow.enabled, nickname: str(meow.nickname), url: str(meow.url), imgUrl: str(meow.imgUrl) },
    qqbot: {
      enabled: !!qqbot.enabled,
      appId: str(qqbot.appId),
      appSecret: str(qqbot.appSecret),
      targetOpenid: str(qqbot.targetOpenid),
      proxy: str(qqbot.proxy),
    },
    telegram: {
      enabled: !!telegram.enabled,
      botToken: str(telegram.botToken),
      chatId: telegram.chatId === null || telegram.chatId === undefined ? "" : String(telegram.chatId),
      adminId: telegram.adminId === null || telegram.adminId === undefined ? "" : String(telegram.adminId),
      baseUrl: str(telegram.baseUrl),
      proxy: str(telegram.proxy),
    },
    webhook: {
      enabled: !!webhook.enabled,
      urls: strList(webhook.urls),
      secret: str(webhook.secret),
      timeoutMs: num(webhook.timeoutMs, 5000),
      retryMax: num(webhook.retryMax, 3),
      textTemplate:
        webhook.textTemplate === null || webhook.textTemplate === undefined
          ? "收到新短信\n设备  {{device_label}}\n收信卡  {{recipient}}\n号码  {{number}}\n时间  {{time}}\n内容  {{text}}"
          : String(webhook.textTemplate),
      headers: recordToHeaderRows(webhook.headers),
    },
    bark: {
      enabled: !!bark.enabled,
      urls: strList(bark.urls),
      urlScopes: Array.isArray(bark.urlScopes)
        ? bark.urlScopes.map(asRecord).map((scope) => ({ url: str(scope.url), deviceIds: strList(scope.deviceIds) }))
        : [],
      group: str(bark.group) || "vocat",
      icon: str(bark.icon),
      level: str(bark.level) || "active",
    },
    email: {
      enabled: !!email.enabled,
      useSsl: !!email.useSsl,
      smtpHost: str(email.smtpHost),
      smtpPort: email.smtpPort === null || email.smtpPort === undefined ? "" : String(email.smtpPort),
      username: str(email.username),
      password: str(email.password),
      fromAddress: str(email.fromAddress),
      toAddresses: joinList(email.toAddresses),
    },
		pushplus: {
			enabled: !!pushplus.enabled,
			token: str(pushplus.token),
			topic: str(pushplus.topic),
			channel: str(pushplus.channel) || "wechat",
		},
		wecom: {
			enabled: !!wecom.enabled,
			urls: strList(wecom.urls),
			payloadTemplate: str(wecom.payloadTemplate ?? wecom.payload_template) || DEFAULT_WECOM_PAYLOAD_TEMPLATE,
		},
		lark: {
			enabled: !!lark.enabled,
			url: str(lark.url),
			signingEnabled: !!lark.signingEnabled,
			secret: lark.signingEnabled ? str(lark.secret) : "",
			payloadTemplate: str(lark.payloadTemplate ?? lark.payload_template) || DEFAULT_LARK_PAYLOAD_TEMPLATE,
		},
	} as NotifyForms;
  for (const channel of Object.keys(forms) as (keyof NotifyForms)[]) {
    const ids = data[channel]?.deviceIds;
    forms[channel].deviceIds = Array.isArray(ids) ? ids.map(String) : null;
  }
  return forms;
}

function splitList(value: string): string[] {
  return value
    ? value
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean)
    : [];
}

export function buildWebhookPayload(form: WebhookForm, forTest = false) {
  const urls = Array.isArray(form.urls) ? form.urls : [];
  return {
    enabled: !!form.enabled,
    urls: forTest ? urls.map((url) => String(url || "").trim()).filter(Boolean) : urls,
    secret: form.secret || "",
    timeoutMs: Number(form.timeoutMs) || 5000,
    retryMax: Number(form.retryMax) || 3,
    textTemplate: String(form.textTemplate || ""),
    headers: headerRowsToRecord(form.headers),
  };
}

export function buildBarkPayload(form: BarkForm, forTest = false) {
  const urls = Array.isArray(form.urls) ? form.urls : [];
  const listed = new Set(urls.map((url) => String(url || "").trim()).filter(Boolean));
  // Drop scopes whose URL was removed or edited away, and keep one per URL; the server rejects both.
  const seen = new Set<string>();
  const urlScopes = (form.urlScopes || [])
    .map((scope) => ({ url: String(scope.url || "").trim(), deviceIds: scope.deviceIds || [] }))
    .filter((scope) => listed.has(scope.url) && !seen.has(scope.url) && seen.add(scope.url));
  return {
    enabled: !!form.enabled,
    urls: forTest ? urls.map((url) => String(url || "").trim()).filter(Boolean) : urls,
    // A test notification goes to every URL, so per-URL device scopes only matter when saving.
    ...(forTest ? {} : { urlScopes }),
    group: String(form.group || "").trim(),
    icon: String(form.icon || "").trim(),
    level: String(form.level || "").trim(),
  };
}

export function buildEmailPayload(form: EmailForm, forTest = false) {
  return {
    enabled: !!form.enabled,
    useSsl: !!form.useSsl,
    smtpHost: forTest ? String(form.smtpHost || "").trim() : form.smtpHost || "",
    smtpPort: Number(form.smtpPort) || 0,
    username: forTest ? String(form.username || "").trim() : form.username || "",
    password: forTest ? String(form.password || "").trim() : form.password || "",
    fromAddress: forTest ? String(form.fromAddress || "").trim() : form.fromAddress || "",
    toAddresses: splitList(form.toAddresses),
  };
}

export function buildWecomPayload(form: WecomForm, forTest = false) {
	const urls = Array.isArray(form.urls) ? form.urls : [];
	return {
		enabled: !!form.enabled,
		urls: forTest ? urls.map((url) => String(url || "").trim()).filter(Boolean) : urls,
		payload_template: String(form.payloadTemplate || ""),
	};
}

export function buildLarkPayload(form: LarkForm, forTest = false) {
	const payload: {
		enabled: boolean;
		url?: string;
		signing_enabled: boolean;
		payload_template: string;
		secret?: string;
	} = {
		enabled: !!form.enabled,
		signing_enabled: !!form.signingEnabled,
		payload_template: String(form.payloadTemplate || ""),
	};
	const url = forTest ? String(form.url || "").trim() : String(form.url || "");
	if (url) payload.url = url;
	if (form.signingEnabled) payload.secret = String(form.secret || "");
	return payload;
}

export function buildMeowPayload(form: NotifyForms["meow"]) {
  return {
    enabled: form.enabled,
    nickname: form.nickname.trim(),
    url: form.url.trim(),
    imgUrl: form.imgUrl.trim(),
  };
}

/** 把 QQ 机器人表单整理为提交给接口的配置（去掉首尾空白）。 */
export function buildQQBotPayload(form: QQBotForm) {
  return {
    enabled: form.enabled,
    appId: form.appId.trim(),
    appSecret: form.appSecret || "",
    targetOpenid: form.targetOpenid.trim(),
    proxy: form.proxy.trim(),
  };
}

export type ClearableNotificationChannel = "telegram" | "email" | "qqbot";

export type QQBotBindStatus = "connecting" | "waiting" | "bound" | "expired" | "failed" | "canceled";

/** 绑定方式：qr 为手机 QQ 扫码，code 为向已配置的机器人私聊发送绑定码。 */
export type QQBotBindMode = "qr" | "code";

export interface QQBotBindState {
  mode: QQBotBindMode;
  code?: string;
  qrUrl?: string;
  expiresAt: string;
  status: QQBotBindStatus;
  error?: string;
}

/** 绑定仍在进行中（需要继续轮询）。 */
export function qqBotBindActive(state: QQBotBindState | null | undefined): boolean {
  return !!state && (state.status === "connecting" || state.status === "waiting");
}

/** 设置页的两种绑定方式：扫码，或手动填写凭据后用绑定码绑定。 */
export type QQBotBindMethod = "qr" | "manual";

/** 默认方式：已保存凭据但还没有接收方时继续手动绑定，否则推荐扫码。 */
export function qqBotDefaultMethod(form: Pick<QQBotForm, "appId" | "appSecret" | "targetOpenid">): QQBotBindMethod {
  const hasCredentials = !!form.appId.trim() && !!form.appSecret;
  return hasCredentials && !form.targetOpenid.trim() ? "manual" : "qr";
}

/** 绑定进行中时固定显示该绑定所用的方式（扫码失败回退到绑定码时切到手动），否则显示用户的选择。 */
export function qqBotVisibleMethod(selected: QQBotBindMethod, bind: QQBotBindState | null | undefined): QQBotBindMethod {
  if (!qqBotBindActive(bind)) return selected;
  return bind!.mode === "qr" ? "qr" : "manual";
}

/** QQ 机器人页的视图：未绑定时显示绑定流程，绑定后显示维护面板。 */
export type QQBotView = "bind" | "maintain";

type QQBotBindingFields = Pick<QQBotForm, "appId" | "appSecret" | "targetOpenid">;

/** 凭据和接收方都已填写。 */
export function qqBotConfigured(form: QQBotBindingFields): boolean {
  return !!form.appId.trim() && !!form.appSecret && !!form.targetOpenid.trim();
}

/**
 * 维护面板可编辑，视图不能直接跟随表单：完整时进入维护面板，三项全部清空（清空配置）时回到绑定流程，
 * 其余情况保持原视图，编辑单个字段不会跳转。
 */
export function qqBotNextView(previous: QQBotView, form: QQBotBindingFields): QQBotView {
  if (qqBotConfigured(form)) return "maintain";
  if (!form.appId.trim() && !form.appSecret && !form.targetOpenid.trim()) return "bind";
  return previous;
}

/** 剩余有效期，格式 m:ss；已过期返回 0:00。 */
export function qqBotBindRemaining(expiresAt: string, now: number = Date.now()): string {
  const seconds = Math.max(0, Math.floor((Date.parse(expiresAt) - now) / 1000));
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
}

export function buildNotificationsPayload(forms: NotifyForms, clearedChannels: readonly ClearableNotificationChannel[] = []) {
  const payload: NotificationSettings = {
    meow: buildMeowPayload(forms.meow),
    qqbot: {
      ...buildQQBotPayload(forms.qqbot),
      ...(clearedChannels.includes("qqbot") ? { clearSecrets: true } : {}),
    },
    telegram: {
      enabled: !!forms.telegram.enabled,
      ...(clearedChannels.includes("telegram") ? { clearSecrets: true } : {}),
      botToken: forms.telegram.botToken || "",
      // vocat 后端将 chat_id/admin_id 存为字符串（参考实现是数字）
      chatId: forms.telegram.chatId.trim(),
      adminId: forms.telegram.adminId.trim(),
      baseUrl: forms.telegram.baseUrl || "",
      proxy: forms.telegram.proxy || "",
    },
    email: {
      ...buildEmailPayload(forms.email),
      ...(clearedChannels.includes("email") ? { clearSecrets: true } : {}),
    },
    pushplus: {
      enabled: !!forms.pushplus.enabled,
      token: forms.pushplus.token || "",
      topic: forms.pushplus.topic || "",
      channel: forms.pushplus.channel || "",
    },
    webhook: buildWebhookPayload(forms.webhook),
		bark: buildBarkPayload(forms.bark),
		wecom: buildWecomPayload(forms.wecom),
		lark: buildLarkPayload(forms.lark),
	};
  for (const channel of Object.keys(forms) as (keyof NotifyForms)[]) {
    payload[channel].deviceIds = forms[channel].deviceIds;
  }
  return payload;
}
