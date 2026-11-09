import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/components/settings/model.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2022,
    target: ts.ScriptTarget.ES2022,
  },
});
const moduleURL = `data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`;
const {
  DEFAULT_LARK_PAYLOAD_TEMPLATE,
  buildBarkPayload,
  buildLarkPayload,
  buildNotificationsPayload,
  defaultNotifyForms,
  formsFromNotifications,
  qqBotBindActive,
  qqBotBindRemaining,
  qqBotConfigured,
  qqBotDefaultMethod,
  qqBotNextView,
  qqBotVisibleMethod,
} = await import(moduleURL);

test("loads a masked signed Lark group bot config", () => {
  const forms = formsFromNotifications({
    lark: {
      enabled: true,
      url: "********",
      signingEnabled: true,
      secret: "********",
      payloadTemplate: '{"msg_type":"text"}',
    },
  });

  assert.deepEqual(forms.lark, {
    deviceIds: null,
    enabled: true,
    url: "********",
    signingEnabled: true,
    secret: "********",
    payloadTemplate: '{"msg_type":"text"}',
  });
});

test("does not expose a dormant signing secret when signing is disabled", () => {
  const forms = formsFromNotifications({
    lark: {
      enabled: true,
      url: "********",
      signingEnabled: false,
      secret: "********",
    },
  });

  assert.equal(forms.lark.secret, "");
  assert.equal(forms.lark.payloadTemplate, DEFAULT_LARK_PAYLOAD_TEMPLATE);
});

test("builds the single group bot webhook contract", () => {
  const signed = buildLarkPayload({
    enabled: true,
    url: "  https://open.larksuite.com/open-apis/bot/v2/hook/token  ",
    signingEnabled: true,
    secret: "demo",
    payloadTemplate: '{"msg_type":"text"}',
  }, true);
  assert.deepEqual(signed, {
    enabled: true,
    url: "https://open.larksuite.com/open-apis/bot/v2/hook/token",
    signing_enabled: true,
    secret: "demo",
    payload_template: '{"msg_type":"text"}',
  });

  const unsigned = buildLarkPayload({
    enabled: false,
    url: "",
    signingEnabled: false,
    secret: "stale-secret",
    payloadTemplate: DEFAULT_LARK_PAYLOAD_TEMPLATE,
  });
  assert.equal(Object.hasOwn(unsigned, "url"), false);
  assert.equal(Object.hasOwn(unsigned, "secret"), false);
});

test("includes Lark in the complete notification settings payload", () => {
  const forms = formsFromNotifications({});
  const payload = buildNotificationsPayload(forms);

  assert.deepEqual(payload.lark, {
    deviceIds: null,
    enabled: false,
    signing_enabled: false,
    payload_template: DEFAULT_LARK_PAYLOAD_TEMPLATE,
  });
});

test("default webhook template identifies the receiving card and preserves custom templates", () => {
  const defaults = formsFromNotifications({});
  assert.match(defaults.webhook.textTemplate, /收信卡  \{\{recipient\}\}/);
  assert.match(buildNotificationsPayload(defaults).webhook.textTemplate, /\{\{recipient\}\}/);

  for (const textTemplate of ["", "legacy {{number}}: {{text}}", "{{local_phone}} / {{iccid}} / {{recipient}}"]) {
    const forms = formsFromNotifications({ webhook: { textTemplate } });
    assert.equal(forms.webhook.textTemplate, textTemplate);
    assert.equal(buildNotificationsPayload(forms).webhook.textTemplate, textTemplate);
  }
});

test("round-trips MeoW alongside the seven existing channels", () => {
  const forms = formsFromNotifications({ meow: { enabled: true, nickname: " 昵称 ", url: " https://example.com ", imgUrl: " https://example.com/icon.png " } });
  assert.deepEqual(buildNotificationsPayload(forms).meow, { enabled: true, nickname: "昵称", url: "https://example.com", imgUrl: "https://example.com/icon.png", deviceIds: null });
  assert.deepEqual(Object.keys(buildNotificationsPayload(forms)).sort(), ["bark", "email", "lark", "meow", "pushplus", "qqbot", "telegram", "webhook", "wecom"].sort());
});

test("clear markers apply only to explicitly cleared Telegram and Email drafts", () => {
  const forms = formsFromNotifications({ telegram: { enabled: true, botToken: "********", deviceIds: [] }, email: { enabled: false, password: "********", deviceIds: ["offline"] } });
  const normal = buildNotificationsPayload(forms);
  assert.equal(Object.hasOwn(normal.telegram, "clearSecrets"), false);
  assert.equal(Object.hasOwn(normal.email, "clearSecrets"), false);
  forms.telegram.botToken = "";
  const cleared = buildNotificationsPayload(forms, ["telegram"]);
  assert.equal(cleared.telegram.enabled, true);
  assert.equal(cleared.telegram.botToken, "");
  assert.equal(cleared.telegram.clearSecrets, true);
  assert.deepEqual(cleared.telegram.deviceIds, []);
  assert.equal(Object.hasOwn(cleared.email, "clearSecrets"), false);
  forms.email.password = "new-password";
  const rewritten = buildNotificationsPayload(forms, ["email"]);
  assert.equal(rewritten.email.enabled, false);
  assert.equal(rewritten.email.password, "new-password");
  assert.equal(rewritten.email.clearSecrets, true);
  assert.deepEqual(rewritten.email.deviceIds, ["offline"]);
});

test("device bindings round-trip missing, null, empty and selected scopes", () => {
  const channels = Object.keys(defaultNotifyForms());
  const scopes = [undefined, null, [], ["offline-device"]];
  const data = Object.fromEntries(channels.map((channel, index) => [channel, { deviceIds: scopes[index % scopes.length] }]));
  const forms = formsFromNotifications(data);
  const payload = buildNotificationsPayload(forms);
  for (const channel of channels) {
    assert.deepEqual(forms[channel].deviceIds, data[channel].deviceIds ?? null);
    assert.deepEqual(payload[channel].deviceIds, data[channel].deviceIds ?? null);
  }
});

test("round-trips Bark per-URL device scopes and drops stale ones", () => {
  const a = "https://api.day.app/KeyA/";
  const b = "https://api.day.app/KeyB/";
  // The API client camelizes keys, so url_scopes/device_ids arrive as urlScopes/deviceIds.
  const forms = formsFromNotifications({
    bark: { enabled: true, urls: [a, b], urlScopes: [{ url: a, deviceIds: ["reader-1"] }] },
  });
  assert.deepEqual(forms.bark.urlScopes, [{ url: a, deviceIds: ["reader-1"] }]);
  assert.deepEqual(defaultNotifyForms().bark.urlScopes, []);

  const payload = buildBarkPayload({
    ...forms.bark,
    urls: [a, ` ${b} `],
    urlScopes: [
      { url: a, deviceIds: ["reader-1"] },
      { url: a, deviceIds: ["reader-9"] },
      { url: b, deviceIds: ["reader-2"] },
      { url: "https://api.day.app/Removed/", deviceIds: ["reader-3"] },
    ],
  });
  assert.deepEqual(payload.urlScopes, [{ url: a, deviceIds: ["reader-1"] }, { url: b, deviceIds: ["reader-2"] }]);
  assert.equal("urlScopes" in buildBarkPayload(forms.bark, true), false);
});

test("round-trips QQ Bot and clears only its secret on request", () => {
  const forms = formsFromNotifications({
    qqbot: { enabled: true, appId: " 102000001 ", appSecret: "********", targetOpenid: " OPENID_1 ", proxy: "", deviceIds: ["a"] },
  });
  assert.deepEqual(buildNotificationsPayload(forms).qqbot, {
    enabled: true, appId: "102000001", appSecret: "********", targetOpenid: "OPENID_1", proxy: "", deviceIds: ["a"],
  });
  assert.equal(Object.hasOwn(formsFromNotifications({ qqbot: { targetType: "group" } }).qqbot, "targetType"), false);
  assert.equal(Object.hasOwn(buildNotificationsPayload(forms).qqbot, "clearSecrets"), false);
  forms.qqbot.appSecret = "";
  const cleared = buildNotificationsPayload(forms, ["qqbot"]);
  assert.equal(cleared.qqbot.clearSecrets, true);
  assert.equal(cleared.qqbot.appSecret, "");
  assert.equal(Object.hasOwn(cleared.telegram, "clearSecrets"), false);
});

test("tracks QQ Bot binding progress", () => {
  assert.equal(qqBotBindActive(null), false);
  assert.equal(qqBotBindActive({ mode: "code", code: "VOCAT-000001", expiresAt: "", status: "connecting" }), true);
  assert.equal(qqBotBindActive({ mode: "code", code: "VOCAT-000001", expiresAt: "", status: "waiting" }), true);
  for (const status of ["bound", "expired", "failed", "canceled"]) {
    assert.equal(qqBotBindActive({ mode: "code", code: "VOCAT-000001", expiresAt: "", status }), false);
  }
  const now = Date.parse("2026-11-09T08:00:00Z");
  assert.equal(qqBotBindRemaining("2026-11-09T08:04:05Z", now), "4:05");
  assert.equal(qqBotBindRemaining("2026-11-09T07:59:00Z", now), "0:00");
});

test("QQ bot binding method follows saved state and active binding", () => {
  assert.equal(qqBotDefaultMethod({ appId: "", appSecret: "", targetOpenid: "" }), "qr");
  assert.equal(qqBotDefaultMethod({ appId: "102000001", appSecret: "********", targetOpenid: "" }), "manual");
  assert.equal(qqBotDefaultMethod({ appId: "102000001", appSecret: "********", targetOpenid: "OPENID_1" }), "qr");

  assert.equal(qqBotVisibleMethod("manual", null), "manual");
  assert.equal(qqBotVisibleMethod("manual", { mode: "qr", expiresAt: "", status: "waiting" }), "qr");
  // 扫码未返回接收方时回退到绑定码，切到手动方式。
  assert.equal(qqBotVisibleMethod("qr", { mode: "code", code: "VOCAT-000001", expiresAt: "", status: "connecting" }), "manual");
  // 结束后恢复用户的选择。
  assert.equal(qqBotVisibleMethod("qr", { mode: "code", code: "VOCAT-000001", expiresAt: "", status: "bound" }), "qr");
});

test("QQ bot view separates binding from maintenance", () => {
  const complete = { appId: "102000001", appSecret: "********", targetOpenid: "OPENID_1" };
  const empty = { appId: "", appSecret: "", targetOpenid: "" };
  assert.equal(qqBotConfigured(complete), true);
  assert.equal(qqBotConfigured({ ...complete, targetOpenid: " " }), false);

  assert.equal(qqBotNextView("bind", complete), "maintain");
  // 维护面板中编辑单个字段不跳转。
  assert.equal(qqBotNextView("maintain", { ...complete, targetOpenid: "" }), "maintain");
  assert.equal(qqBotNextView("maintain", { ...complete, appSecret: "" }), "maintain");
  // 清空配置回到绑定流程。
  assert.equal(qqBotNextView("maintain", empty), "bind");
  // 半完成状态（手动第 1 步、扫码只保存了凭据）留在绑定流程。
  assert.equal(qqBotNextView("bind", { ...complete, targetOpenid: "" }), "bind");
  assert.equal(qqBotNextView("bind", empty), "bind");
});
