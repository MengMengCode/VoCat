import type { SSOConfigInput, SSOProviderKind, SSOSaveRequest } from "../types";

/** Create a disabled provider draft with Pocket ID defaults and no credentials. */
export function defaultSSOConfig(origin = ""): SSOConfigInput {
  return {
    enabled: false,
    kind: "pocketid",
    displayName: "Pocket ID",
    publicOrigin: origin,
    clientId: "",
    issuerUrl: "",
    authUrl: "",
    tokenUrl: "",
    userInfoUrl: "",
    userIdField: "",
    scopes: ["openid", "profile", "email"],
    tokenAuthMethod: "auto",
    pkceMode: "s256",
  };
}

/** Preview only: authorization continues to use the saved server configuration. */
export function ssoCallbackPreview(publicOrigin: string): string | null {
  if (!publicOrigin || publicOrigin.trim() !== publicOrigin || new TextEncoder().encode(publicOrigin).length > 4096
    || /[\p{Cc}\\?#%]/u.test(publicOrigin)) return null;
  // Check the raw path before URL can normalize dot segments or extra slashes.
  const match = /^(https?):\/\/(\[[^\]]+\]|[^:/@\[\]]+)(?::([0-9]+))?\/?$/i.exec(publicOrigin);
  if (!match) return null;
  const [, scheme, rawHost, port] = match;
  if (port !== undefined && (Number(port) < 1 || Number(port) > 65535)) return null;
  try {
    const parsed = new URL(publicOrigin);
    // Browsers send the WHATWG-compressed IPv6 spelling in Host.
    const host = rawHost.startsWith("[") ? parsed.hostname : rawHost.toLowerCase();
    if (!host.startsWith("[") && parsed.hostname !== host) return null;
    if (parsed.protocol === "http:") {
      const octets = host.split(".");
      const ipv4Local = octets.length === 4 && (octets[0] === "127" || octets[0] === "10"
        || (octets[0] === "192" && octets[1] === "168")
        || (octets[0] === "172" && Number(octets[1]) >= 16 && Number(octets[1]) <= 31))
        && octets.every((part) => /^(0|[1-9][0-9]{0,2})$/.test(part) && Number(part) <= 255);
      const ipv6Local = host.startsWith("[") && (parsed.hostname === "[::1]" || /^\[f[cd][0-9a-f]{2}:/.test(parsed.hostname)
        || /^\[::ffff:(?:7f[0-9a-f]{2}|a[0-9a-f]{2}|ac1[0-9a-f]|c0a8):[0-9a-f]{1,4}\]$/.test(parsed.hostname));
      if (host !== "localhost" && !ipv4Local && !ipv6Local) return null;
    }
    // Match the browser entry Host, preserving explicit nonstandard ports.
    const suffix = port && !((parsed.protocol === "https:" && port === "443") || (parsed.protocol === "http:" && port === "80")) ? `:${port}` : "";
    return `${scheme.toLowerCase()}://${host}${suffix}/api/auth/sso/callback`;
  } catch {
    return null;
  }
}

/** Copy only writable fields; GET metadata must never leak into a PUT. */
export function ssoConfigInput(value: SSOConfigInput): SSOConfigInput {
  const common = {
    enabled: !!value.enabled,
    kind: value.kind,
    displayName: value.displayName ?? "",
    publicOrigin: value.publicOrigin ?? "",
    clientId: value.clientId ?? "",
    issuerUrl: value.kind === "pocketid" ? value.issuerUrl ?? "" : "",
    authUrl: value.kind === "generic" ? value.authUrl ?? "" : "",
    tokenUrl: value.kind === "generic" ? value.tokenUrl ?? "" : "",
    userInfoUrl: value.kind === "generic" ? value.userInfoUrl ?? "" : "",
    userIdField: value.kind === "generic" ? value.userIdField ?? "" : "",
    scopes: [...new Set(value.scopes ?? [])],
    tokenAuthMethod: value.tokenAuthMethod,
    pkceMode: value.kind === "generic" ? value.pkceMode : "s256" as const,
  };
  return common;
}

/** Reset provider-specific fields and credentials-related choices when the identity provider changes. */
export function changeSSOProvider(value: SSOConfigInput, kind: SSOProviderKind): SSOConfigInput {
  return {
    ...defaultSSOConfig(value.publicOrigin),
    kind,
    displayName: kind === "pocketid" ? "Pocket ID" : kind === "github" ? "GitHub" : "OAuth2",
    scopes: kind === "pocketid" ? ["openid", "profile", "email"] : kind === "github" ? ["read:user"] : [],
    tokenAuthMethod: kind === "pocketid" ? "auto" : kind === "generic" ? "client_secret_post" : "client_secret_basic",
    userIdField: kind === "generic" ? "id" : "",
  };
}

/** Compare fields that define an identity namespace, ignoring display and transport settings. */
export function ssoIdentitySourceChanged(before: SSOConfigInput, after: SSOConfigInput): boolean {
  if (before.kind !== after.kind || before.clientId !== after.clientId) return true;
  if (after.kind === "pocketid") return before.issuerUrl !== after.issuerUrl;
  if (after.kind === "generic") {
    return before.authUrl !== after.authUrl || before.tokenUrl !== after.tokenUrl
      || before.userInfoUrl !== after.userInfoUrl || before.userIdField !== after.userIdField;
  }
  return false;
}

/** Build a revision-checked save request, omitting an unchanged secret and rejecting mask values. */
export function buildSSOSaveRequest(
  config: SSOConfigInput,
  expectedRevision: number,
  secret: string,
  resetBinding = false,
): SSOSaveRequest {
  if (secret && (/^[*•●\s]+$/.test(secret) || !secret.trim())) throw new Error("sso_invalid_secret");
  return {
    expectedRevision,
    config: ssoConfigInput(config),
    ...(secret ? { clientSecret: secret } : {}),
    ...(resetBinding ? { resetBinding: true } : {}),
  };
}

export const SSO_ERROR_MESSAGES: Record<string, string> = {
  access_denied: "已取消身份验证，可以手动重试。",
  invalid_flow: "授权请求无效或已使用，请重新发起。",
  flow_expired: "授权请求已过期，请重新发起。",
  config_changed: "SSO 配置已变化，请重新加载后重试。",
  identity_not_bound: "此外部账号尚未绑定到当前管理员。请先使用密码登录并绑定。",
  binding_exists: "已绑定外部账号，请先解绑后再绑定。",
  provider_unavailable: "身份提供方暂时不可用，请稍后重试或使用密码登录。",
  invalid_identity: "身份验证未通过，请重新发起或使用密码登录。",
};

export const SSO_RESULT_MESSAGES: Record<string, string> = {
  bound: "外部账号已绑定。确认绑定状态后可启用 SSO 登录。",
  unbound: "外部账号已解绑，SSO 登录已停用。",
  config_saved: "SSO 配置已保存，请重新登录。",
};

/** Map callback categories through an own-property whitelist to safe display messages. */
export function ssoErrorMessage(code: string): string {
  return Object.hasOwn(SSO_ERROR_MESSAGES, code) ? SSO_ERROR_MESSAGES[code] : "SSO 操作未完成，请重新发起。";
}
