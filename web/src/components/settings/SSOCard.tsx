import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { CheckmarkRegular, KeyRegular } from "@fluentui/react-icons";
import {
  ApiError, bindSSOAccount, checkSSOSettings, getSSOSettings,
  notifyUnauthorized, saveSSOSettings, unbindSSOAccount,
} from "../../api";
import type { SSOConfigInput, SSOProviderKind, SSOSettings } from "../../types";
import { useI18n } from "../../lib/i18n";
import {
  buildSSOSaveRequest, changeSSOProvider, defaultSSOConfig, ssoCallbackPreview, ssoConfigInput,
  ssoErrorMessage, ssoIdentitySourceChanged, SSO_RESULT_MESSAGES,
} from "../../lib/sso";
import { Button, Input, Switch, confirmDialog, message } from "../ui";
import { CardDecor, CardIcon, CardTitle } from "./Cards";
import { copyText, Field, PasswordInput } from "./controls";

const SELECT_CLASS = "h-8 w-full rounded-lg border border-[#dcdfe6] bg-white px-3 text-sm text-gray-900 outline-none focus:border-sky-500 focus:ring-2 focus:ring-sky-500/25 disabled:opacity-60 dark:border-white/15 dark:bg-gray-900 dark:text-gray-100";
const EMPTY_BINDING = { bound: false, displayName: "", email: "", createdAt: null, lastLoginAt: null };

/** Manage saved SSO settings, account binding, and readiness checks while keeping unsaved edits local. */
export function SSOCard() {
  const { t, lang } = useI18n();
  const [searchParams, setSearchParams] = useSearchParams();
  const [saved, setSaved] = useState<SSOSettings | null>(null);
  const [draft, setDraft] = useState<SSOConfigInput>(() => defaultSSOConfig(window.location.origin));
  const [secret, setSecret] = useState("");
  const [scopeText, setScopeText] = useState("openid profile email");
  const [loading, setLoading] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  const [working, setWorking] = useState<"save" | "check" | "bind" | "unbind" | null>(null);
  const [checkedRevision, setCheckedRevision] = useState<number | null>(null);
  const [operationError, setOperationError] = useState<string | null>(null);
  const [callbackNotice, setCallbackNotice] = useState<{ error: boolean; text: string } | null>(null);

  /** Apply authoritative settings and clear transient secret and readiness state. */
  const applySaved = useCallback((data: SSOSettings) => {
    setSaved(data);
    const input = ssoConfigInput({ ...defaultSSOConfig(), ...data.config });
    setDraft(data.config.revision === 0 ? { ...input, publicOrigin: window.location.origin } : input);
    setScopeText(input.scopes.join(" "));
    setSecret("");
    setCheckedRevision(null);
  }, []);

  /** Load saved settings and expose a retryable error without discarding the settings page. */
  const load = useCallback(async () => {
    setLoading(true);
    setLoadFailed(false);
    try {
      applySaved(await getSSOSettings());
    } catch {
      setLoadFailed(true);
    } finally {
      setLoading(false);
    }
  }, [applySaved]);

  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    const error = searchParams.get("sso_error");
    const result = searchParams.get("sso_result");
    if (!error && !result) return;
    if (error) setCallbackNotice({ error: true, text: ssoErrorMessage(error) });
    else if (result && Object.hasOwn(SSO_RESULT_MESSAGES, result)) setCallbackNotice({ error: false, text: SSO_RESULT_MESSAGES[result] });
    const next = new URLSearchParams(searchParams);
    next.delete("sso_error");
    next.delete("sso_result");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  const revision = saved?.config.revision ?? 0;
  const baseline = saved ? ssoConfigInput({ ...defaultSSOConfig(), ...saved.config }) : defaultSSOConfig();
  const sourceChanged = saved ? ssoIdentitySourceChanged(baseline, draft) : false;
  const dirty = JSON.stringify(ssoConfigInput(draft)) !== JSON.stringify(baseline) || !!secret;
  const busy = loading || working !== null;
  const binding = saved?.binding ?? EMPTY_BINDING;
  const canUseSaved = !!saved?.configured && revision > 0 && !dirty && !busy;
  const callbackPreview = ssoCallbackPreview(draft.publicOrigin);
  const originChanged = revision > 0 && callbackPreview !== ssoCallbackPreview(baseline.publicOrigin);

  /** Apply a local form change and clear the previous operation error. */
  function update(patch: Partial<SSOConfigInput>) {
    setOperationError(null);
    setDraft((current) => ({ ...current, ...patch }));
  }

  /** Translate known operation errors without displaying raw server or provider responses. */
  function fail(error: unknown) {
    const code = error instanceof ApiError ? error.code : error instanceof Error ? error.message : "";
    const translated: Record<string, string> = {
      sso_config_changed: "SSO 配置已变化，请重新加载后重试。",
      sso_rebind_required: "身份源已变化，需要清除旧绑定并重新绑定。",
      sso_binding_exists: "已绑定外部账号，请先解绑后再绑定。",
      sso_not_configured: "请先补全并保存 SSO 配置和 Client Secret。",
      sso_identity_not_bound: "请先绑定外部账号，再启用 SSO 登录。",
      invalid_sso_config: "SSO 配置无效，请检查地址、Scope、PKCE 和 HTTPS Cookie 设置。",
      sso_disabled: "SSO 登录尚未启用。",
      sso_provider_unavailable: "身份提供方暂时不可用，请稍后重试或使用密码登录。",
      sso_rate_limited: "操作过于频繁，请稍后重试。",
      sso_invalid_secret: "请填写有效的 Client Secret，不能提交掩码。",
      session_required: "请使用浏览器管理员会话管理 SSO。",
    };
    setOperationError(Object.hasOwn(translated, code) ? translated[code] : "SSO 操作未完成，请检查配置后重试。");
  }

  /** Reset client authentication and preserve the successful operation notice across document navigation. */
  function reauthenticate(result: "config_saved" | "unbound") {
    notifyUnauthorized();
    // A document navigation also resets pending protected-page requests. A
    // client-only navigation can race RequireAuth and discard this result.
    window.location.replace(`/login?${new URLSearchParams({ sso_result: result, redirect: "/settings" }).toString()}`);
  }

  /** Confirm identity-source resets and submit writable settings against the saved revision. */
  async function save() {
    if (!saved) return;
    setOperationError(null);
    let resetBinding = false;
    if (sourceChanged && binding.bound) {
      resetBinding = await confirmDialog(
        t("身份源变化会清除旧绑定并关闭 SSO 登录。请重新填写密钥，保存后重新绑定外部账号。"),
        t("更换身份源"),
        { confirmText: t("清除绑定并保存"), confirmVariant: "danger", type: "warning" },
      );
      if (!resetBinding) return;
    }
    if (sourceChanged && saved.hasClientSecret && !secret) {
      setOperationError("身份源已变化，请重新填写 Client Secret。");
      return;
    }
    setWorking("save");
    try {
      const result = await saveSSOSettings(buildSSOSaveRequest(
        sourceChanged ? { ...draft, enabled: false } : draft, revision, secret, resetBinding,
      ));
      message.success(t("SSO 配置已保存"));
      if (result.reauthenticationRequired) reauthenticate("config_saved");
      else applySaved(result);
    } catch (error) {
      fail(error);
    } finally {
      setWorking(null);
    }
  }

  /** Check only the unmodified saved revision and clear stale readiness results on failure. */
  async function check() {
    if (!canUseSaved) return;
    setWorking("check");
    setOperationError(null);
    try {
      const result = await checkSSOSettings(revision);
      setCheckedRevision(result.revision);
      message.success(t("配置检查通过"));
    } catch (error) {
      setCheckedRevision(null);
      fail(error);
    } finally {
      setWorking(null);
    }
  }

  /** Start binding from saved settings and navigate to the server-generated authorization address. */
  async function bind() {
    if (!canUseSaved || binding.bound) return;
    setWorking("bind");
    setOperationError(null);
    try {
      const result = await bindSSOAccount(revision);
      // The server generated this address from the saved provider. Perform a
      // top-level navigation so the flow cookie survives the IdP round trip.
      window.location.assign(result.authorizationUrl);
    } catch (error) {
      fail(error);
      setWorking(null);
    }
  }

  /** Confirm account removal and refresh settings or reauthenticate after session revocation. */
  async function unbind() {
    if (!saved || !binding.bound || !await confirmDialog(
      t("解绑会立即关闭 SSO 登录，并撤销此外部账号建立的所有 SSO 会话。"),
      t("确认解绑外部账号"),
      { confirmText: t("解绑账号"), confirmVariant: "danger", type: "warning" },
    )) return;
    setWorking("unbind");
    setOperationError(null);
    try {
      const result = await unbindSSOAccount(revision);
      message.success(t("外部账号已解绑，SSO 登录已停用。"));
      if (result.reauthenticationRequired) reauthenticate("unbound");
      else await load();
    } catch (error) {
      fail(error);
    } finally {
      setWorking(null);
    }
  }

  /** Render a translated provider field that updates the local draft and respects the busy state. */
  function textField(label: string, key: "displayName" | "publicOrigin" | "clientId" | "issuerUrl" | "authUrl" | "tokenUrl" | "userInfoUrl" | "userIdField", placeholder?: string, hint?: string) {
    return <Field label={t(label)} hint={hint}>
      <Input aria-label={t(label)} value={draft[key]} disabled={busy} placeholder={placeholder} autoComplete="off" onChange={(event) => update({ [key]: event.target.value })} />
    </Field>;
  }

  /** Format binding timestamps in the selected locale, including the never-used state. */
  function time(value: string | null) {
    if (!value) return t("从未使用");
    return new Date(value).toLocaleString(lang === "zh" ? "zh-CN" : "en-US");
  }

  return (
    <section aria-label={t("单点登录（SSO）")} className="ui-card group relative min-w-0 overflow-hidden p-5 sm:p-8 lg:col-span-2">
      <CardDecor />
      <div className="relative z-10 mb-6 flex flex-col justify-between gap-4 sm:flex-row sm:items-center">
        <div className="flex items-center gap-3">
          <CardIcon><KeyRegular className="text-[24px]" /></CardIcon>
          <CardTitle title={t("单点登录（SSO）")} subtitle={t("先绑定管理员账号，再开放外部身份登录")} />
        </div>
        <Button variant="primary" loading={working === "save"} disabled={busy || !saved || !dirty} onClick={() => void save()} icon={<CheckmarkRegular />}>
          {t("保存 SSO 配置")}
        </Button>
      </div>

      {loading ? <p className="relative z-10 text-sm text-gray-500">{t("正在加载 SSO 配置…")}</p> : loadFailed ? (
        <div className="relative z-10 flex items-center justify-between gap-3 rounded-lg bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
          <span>{t("SSO 配置加载失败")}</span><Button onClick={() => void load()}>{t("重试")}</Button>
        </div>
      ) : (
        <div className="relative z-10 space-y-6">
          {callbackNotice ? <p role={callbackNotice.error ? "alert" : "status"} className="rounded-lg bg-sky-50 p-3 text-sm text-sky-800 dark:bg-sky-500/10 dark:text-sky-200">{t(callbackNotice.text)}</p> : null}
          {operationError ? <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
            <span>{t(operationError)}</span>
            {operationError === "SSO 配置已变化，请重新加载后重试。" ? <Button disabled={busy} onClick={() => void load()}>{t("重新加载")}</Button> : null}
          </div> : null}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label={t("身份提供方")}>
              <select aria-label={t("身份提供方")} className={SELECT_CLASS} value={draft.kind} disabled={busy} onChange={(event) => {
                const next = changeSSOProvider(draft, event.target.value as SSOProviderKind);
                setDraft(next);
                setScopeText(next.scopes.join(" "));
                setSecret("");
                setOperationError(null);
              }}>
                <option value="pocketid">Pocket ID</option><option value="github">GitHub</option><option value="generic">Generic OAuth2</option>
              </select>
            </Field>
            {textField("VoCat 公开地址", "publicOrigin", "https://vocat.example.com", t("使用当前访问入口完成登录；请在身份服务登记域名和内网地址各自的回调 URL。"))}
            {draft.kind === "pocketid" ? textField("Pocket ID Issuer", "issuerUrl", "https://pocket-id.example.com", t("填写 Pocket ID 的实例地址")) : null}
            {textField("Client ID", "clientId", t("输入 Client ID"), t("填写身份服务中为 VoCat 创建的客户端 ID"))}
            <Field label="Client Secret" hint={t(saved?.hasClientSecret && !sourceChanged ? "已保存密钥；留空表示保持不变。" : "填写客户端密钥；仅保存在服务器，不会回显。") }>
              <PasswordInput value={secret} disabled={busy} placeholder={t("输入新密钥")} autoComplete="new-password" onChange={(value) => { setSecret(value); setOperationError(null); }} />
            </Field>
            {textField("登录按钮名称", "displayName")}
            {draft.kind === "generic" ? <>
              {textField("授权地址", "authUrl", "https://id.example.com/authorize")}
              {textField("Token 地址", "tokenUrl", "https://id.example.com/token")}
              {textField("UserInfo 地址", "userInfoUrl", "https://id.example.com/userinfo")}
              {textField("用户 ID 字段", "userIdField", "id")}
              <Field label={t("Scopes（空格分隔）")}>
                <Input aria-label={t("Scopes（空格分隔）")} value={scopeText} disabled={busy} onChange={(event) => { setScopeText(event.target.value); update({ scopes: event.target.value.split(/\s+/).filter(Boolean) }); }} />
              </Field>
              <Field label={t("Token 客户端认证")}>
                <select aria-label={t("Token 客户端认证")} className={SELECT_CLASS} value={draft.tokenAuthMethod} disabled={busy} onChange={(event) => update({ tokenAuthMethod: event.target.value as SSOConfigInput["tokenAuthMethod"] })}>
                  <option value="client_secret_basic">client_secret_basic</option><option value="client_secret_post">client_secret_post</option>
                </select>
              </Field>
              <Field label="PKCE" hint={t("仅旧版身份服务不支持 PKCE 时才关闭。") }>
                <select aria-label="PKCE" className={SELECT_CLASS} value={draft.pkceMode} disabled={busy} onChange={(event) => update({ pkceMode: event.target.value as SSOConfigInput["pkceMode"] })}>
                  <option value="s256">S256</option><option value="disabled">{t("关闭")}</option>
                </select>
              </Field>
            </> : null}
          </div>

          <Field label={t("回调地址")} hint={callbackPreview && originChanged ? t("访问地址有未保存的修改，保存后生效。") : undefined}>
            <div className="flex min-w-0 items-start gap-3">
              <p className="min-w-0 flex-1 break-all py-1 font-mono text-xs text-gray-700 dark:text-gray-200">{callbackPreview || t("请填写有效的 VoCat 公开地址以生成回调地址。")}</p>
              <Button size="small" className="shrink-0" disabled={!callbackPreview} onClick={() => void copyText(callbackPreview)}>{t("复制")}</Button>
            </div>
          </Field>

          {sourceChanged && revision > 0 ? <p className="rounded-lg bg-amber-50 p-3 text-xs leading-relaxed text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">{t("身份源变化后旧密钥不会继承。保存时将停用 SSO；已有绑定需要确认清除，然后重新绑定。")}</p> : null}

          <div className="space-y-4 border-t border-gray-200 pt-5 dark:border-white/10">
            <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
              <div className="min-w-0 space-y-1">
                <h4 className="text-sm font-semibold">{t("管理员外部账号")}</h4>
                {binding.bound ? <>
                  <p className="break-all text-sm">{binding.displayName || t("已绑定外部账号")}</p>
                  {binding.email ? <p className="break-all text-xs text-gray-500">{binding.email}</p> : null}
                  <p className="text-xs text-gray-500">{t("绑定时间")}：{time(binding.createdAt)}</p>
                  <p className="text-xs text-gray-500">{t("上次 SSO 登录")}：{time(binding.lastLoginAt)}</p>
                </> : <p className="text-sm text-gray-500">{t("尚未绑定。先保存完整配置，再检查并绑定当前管理员的外部账号。")}</p>}
              </div>
              <div className="flex flex-wrap gap-2">
                <Button disabled={!canUseSaved} loading={working === "check"} onClick={() => void check()}>{t("检查配置")}</Button>
                {binding.bound ? (
                  <Button variant="danger" plain disabled={busy || dirty} loading={working === "unbind"} onClick={() => void unbind()}>{t("解绑账号")}</Button>
                ) : (
                  <Button variant="primary" plain disabled={!canUseSaved} loading={working === "bind"} onClick={() => void bind()}>{t("绑定外部账号")}</Button>
                )}
              </div>
            </div>
            {dirty ? <p className="text-xs text-amber-700 dark:text-amber-300">{t("有未保存的修改。请先保存，再检查配置、绑定或解绑。")}</p> : null}
            {checkedRevision === revision ? <p role="status" className="text-xs text-emerald-700 dark:text-emerald-300">{t("配置检查通过")}</p> : null}
            <div className="flex items-start justify-between gap-4 rounded-lg bg-gray-50 p-4 dark:bg-white/5">
              <div><p className="text-sm font-semibold">{t("开放 SSO 登录")}</p><p className="mt-1 text-xs leading-relaxed text-gray-500">{t("完成绑定后启用并保存，登录页才会显示 SSO 入口。本地密码登录始终保留。")}</p></div>
              <Switch ariaLabel={t("开放 SSO 登录")} checked={draft.enabled} disabled={busy || !binding.bound || sourceChanged} onChange={(enabled) => update({ enabled })} />
            </div>
            <p className="text-xs leading-relaxed text-gray-500">{t("每次保存配置都会撤销现有 SSO 会话，需要重新登录。密码登录会话和 API 令牌继续有效。")}</p>
          </div>
        </div>
      )}
    </section>
  );
}
