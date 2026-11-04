import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/lib/sso.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.ES2022, target: ts.ScriptTarget.ES2022 } });
const { defaultSSOConfig, changeSSOProvider, ssoCallbackPreview, ssoIdentitySourceChanged, buildSSOSaveRequest, ssoErrorMessage } = await import(`data:text/javascript;base64,${Buffer.from(compiled.outputText).toString("base64")}`);

test("Pocket ID starts as an unsaved disabled draft with PKCE and automatic client authentication", () => {
  const config = defaultSSOConfig("https://vocat.example.com");
  assert.equal(config.kind, "pocketid");
  assert.equal(config.enabled, false);
  assert.equal(config.pkceMode, "s256");
  assert.equal(config.tokenAuthMethod, "auto");
  assert.deepEqual(config.scopes, ["openid", "profile", "email"]);
});

test("callback URLs are available before saving credentials, including loopback development", () => {
  for (const origin of ["http://localhost:5173", "http://127.0.0.1:8080", "http://192.168.111.27:7575", "http://10.0.0.2", "http://172.16.0.2", "http://[fd00::1]:7575", "http://[::1]:8080", "https://vocat.nas.test:8443"]) {
    const draft = defaultSSOConfig(origin);
    assert.equal(draft.clientId, "");
    assert.equal(ssoCallbackPreview(draft.publicOrigin), `${origin}/api/auth/sso/callback`);
  }
});

test("callback preview normalizes origins using the server's rules", () => {
  for (const [input, origin] of [
    ["HTTPS://VoCat.Example:443/", "https://vocat.example"],
    ["http://LOCALHOST:80/", "http://localhost"],
    ["http://127.12.34.56:8080/", "http://127.12.34.56:8080"],
    ["http://[0:0:0:0:0:0:0:1]:8080/", "http://[::1]:8080"],
    ["http://[::ffff:127.0.0.1]:8080", "http://[::ffff:7f00:1]:8080"],
    ["https://VoCat.Example:0443/", "https://vocat.example:0443"],
  ]) assert.equal(ssoCallbackPreview(input), `${origin}/api/auth/sso/callback`, input);
});

test("unsaved origin edits update the callback preview without changing the saved configuration", () => {
  const saved = { ...defaultSSOConfig("https://canonical.example"), revision: 7 };
  const draft = { ...saved, publicOrigin: "https://vocat.nas.test:8443" };
  assert.equal(ssoCallbackPreview(draft.publicOrigin), "https://vocat.nas.test:8443/api/auth/sso/callback");
  assert.equal(ssoCallbackPreview(saved.publicOrigin), "https://canonical.example/api/auth/sso/callback");
  assert.equal(saved.revision, 7);
});

test("invalid public origins never produce a copyable callback URL", () => {
  for (const origin of [
    "", "/", "vocat.example", "//vocat.example", "https:vocat.example", "ftp://vocat.example",
    "https://vocat.example/vocat", "https://vocat.example/./", "https://vocat.example/a/../", "https://vocat.example/%2e/",
    "https://vocat.example?", "https://vocat.example/?query=1", "https://vocat.example#", "https://vocat.example/#fragment",
    "https://user:secret@vocat.example", "https://@vocat.example", "https://vocat.example\\path",
    " https://vocat.example", "https://vocat.example ", "https://vocat.\nexample", "https://vocat.example\u0085",
    "https://vocat.example:0", "https://vocat.example:", "https://vocat.example:65536", "https://[::1",
    "http://vocat.example", "http://localhost.",
    "http://127.1", "http://127.00.0.1", "http://2130706433", "http://0x7f000001", "http://[::2]",
  ]) assert.equal(ssoCallbackPreview(origin), null, origin);
});

test("saving omits the unchanged secret and never sends read-only metadata", () => {
  const config = { ...defaultSSOConfig(), revision: 3, providerInstanceId: "private-server-namespace" };
  const body = buildSSOSaveRequest(config, 3, "");
  assert.equal(Object.hasOwn(body, "clientSecret"), false);
  assert.equal(Object.hasOwn(body, "clearClientSecret"), false);
  assert.equal(Object.hasOwn(body.config, "revision"), false);
  assert.equal(Object.hasOwn(body.config, "providerInstanceId"), false);
  assert.equal(body.expectedRevision, 3);
  assert.equal(buildSSOSaveRequest(config, 3, "new-secret").clientSecret, "new-secret");
  assert.equal(Object.hasOwn(buildSSOSaveRequest({ ...config, enabled: true }, 3, ""), "clientSecret"), false);
});

test("secret replacement rejects masks and binding reset remains explicit", () => {
  const config = defaultSSOConfig();
  for (const mask of ["********", "••••••", "●●●", "   ", "  ****  "]) {
    assert.throws(() => buildSSOSaveRequest(config, 1, mask), /sso_invalid_secret/);
  }
  assert.equal(Object.hasOwn(buildSSOSaveRequest(config, 1, ""), "resetBinding"), false);
  const reset = buildSSOSaveRequest(config, 1, "new-secret", true);
  assert.equal(reset.resetBinding, true);
  assert.equal(reset.clientSecret, "new-secret");
  assert.equal(Object.hasOwn(reset, "clearClientSecret"), false);
});

test("provider changes discard endpoint fields and use the new provider defaults", () => {
  const generic = changeSSOProvider(defaultSSOConfig(), "generic");
  Object.assign(generic, { authUrl: "https://old.example/auth", tokenUrl: "https://old.example/token", userInfoUrl: "https://old.example/user", clientId: "old", enabled: true });
  const pocket = changeSSOProvider(generic, "pocketid");
  assert.equal(pocket.authUrl, "");
  assert.equal(pocket.clientId, "");
  assert.equal(pocket.enabled, false);
  assert.equal(pocket.pkceMode, "s256");
  assert.equal(pocket.tokenAuthMethod, "auto");
  const payload = buildSSOSaveRequest({ ...pocket, userIdField: "Old_Claim_Name" }, 3, "");
  assert.equal(payload.config.userIdField, "");
  const genericPayload = buildSSOSaveRequest({ ...generic, userIdField: "User_ID" }, 3, "");
  assert.equal(genericPayload.config.userIdField, "User_ID");
});

test("saving a label edit preserves saved origin and hidden Pocket ID protocol settings", () => {
  for (const tokenAuthMethod of ["client_secret_basic", "client_secret_post", "auto"]) {
    const saved = {
      ...defaultSSOConfig("https://canonical.example"),
      clientId: "saved-client",
      issuerUrl: "https://id.example",
      scopes: ["openid", "email"],
      tokenAuthMethod,
    };
    const request = buildSSOSaveRequest({ ...saved, displayName: "New label" }, 12, "");
    assert.deepEqual(request.config, { ...saved, displayName: "New label" });
    assert.equal(request.expectedRevision, 12);
    assert.equal(Object.hasOwn(request, "clientSecret"), false);
  }
});

test("Generic retains explicitly configured endpoints and protocol settings", () => {
  const saved = {
    ...changeSSOProvider(defaultSSOConfig("https://canonical.example"), "generic"),
    clientId: "generic-client",
    authUrl: "https://id.example/authorize",
    tokenUrl: "http://192.168.1.20:1411/token",
    userInfoUrl: "http://id.nas.example:1411/userinfo",
    userIdField: "External_ID",
    scopes: ["profile", "custom"],
    tokenAuthMethod: "client_secret_basic",
    pkceMode: "disabled",
  };
  const request = buildSSOSaveRequest({ ...saved, displayName: "New label" }, 8, "");
  assert.deepEqual(request.config, { ...saved, displayName: "New label" });
  assert.equal(Object.hasOwn(request, "clientSecret"), false);
});

test("localhost, NAS and public VoCat origins do not change explicitly configured IdP addresses", () => {
  for (const publicOrigin of ["http://localhost:5173", "https://vocat.nas.example:8443", "https://vocat.example"]) {
    const pocket = { ...defaultSSOConfig(publicOrigin), issuerUrl: "https://id.nas.example:8443" };
    const pocketRequest = buildSSOSaveRequest(pocket, 4, "");
    assert.equal(pocketRequest.config.publicOrigin, publicOrigin);
    assert.equal(pocketRequest.config.issuerUrl, pocket.issuerUrl);
    const generic = {
      ...changeSSOProvider(pocket, "generic"),
      authUrl: "https://id.nas.example:8443/authorize",
      tokenUrl: "http://192.168.1.20:1411/token",
      userInfoUrl: "http://id.nas.example:1411/userinfo",
    };
    assert.deepEqual(buildSSOSaveRequest(generic, 4, "").config, generic);
  }
});

test("SSO save payload contains only the supported provider fields", () => {
  const config = { ...defaultSSOConfig(), arbitraryNetworkPermission: true };
  const request = buildSSOSaveRequest(config, 4, "");
  assert.deepEqual(Object.keys(request.config).sort(), [
    "enabled", "kind", "displayName", "publicOrigin", "clientId", "issuerUrl", "authUrl", "tokenUrl", "userInfoUrl",
    "userIdField", "scopes", "tokenAuthMethod", "pkceMode",
  ].sort());
  assert.equal(Object.hasOwn(request.config, "arbitraryNetworkPermission"), false);
});

test("only identity source changes require rebinding", () => {
  const pocket = { ...defaultSSOConfig(), clientId: "vocat", issuerUrl: "https://id.example" };
  assert.equal(ssoIdentitySourceChanged(pocket, { ...pocket, displayName: "Custom", scopes: ["openid"], publicOrigin: "https://new-vocat.example" }), false);
  assert.equal(ssoIdentitySourceChanged(pocket, { ...pocket, issuerUrl: "https://id.example/" }), true);
  assert.equal(ssoIdentitySourceChanged(pocket, { ...pocket, clientId: "new" }), true);
  const generic = changeSSOProvider(pocket, "generic");
  for (const field of ["authUrl", "tokenUrl", "userInfoUrl", "userIdField", "clientId"]) {
    assert.equal(ssoIdentitySourceChanged(generic, { ...generic, [field]: "changed" }), true, field);
  }
});

test("callback errors use a fixed whitelist, including prototype keys", () => {
  assert.match(ssoErrorMessage("access_denied"), /取消/);
  assert.match(ssoErrorMessage("binding_exists"), /先解绑/);
  for (const code of ["<img src=x>", "__proto__", "constructor", "raw-secret"]) {
    assert.equal(ssoErrorMessage(code), "SSO 操作未完成，请重新发起。");
  }
});

// Cover all accepted IPv4-mapped private ranges and adjacent rejected boundaries.
test("mapped IPv6 callback previews agree with the backend private IPv4 policy", () => {
  for (const address of ["10.0.0.0", "10.255.255.255", "127.0.0.1", "172.16.0.0", "172.31.255.255", "192.168.0.0", "192.168.255.255", "a00:2", "ac10:2", "c0a8:114"]) {
    const origin = `http://[::ffff:${address}]:7575`;
    assert.equal(ssoCallbackPreview(origin), `${new URL(origin).origin}/api/auth/sso/callback`, address);
  }
  for (const address of ["9.255.255.255", "11.0.0.0", "126.255.255.255", "128.0.0.0", "172.15.255.255", "172.32.0.0", "192.167.255.255", "192.169.0.0", "169.254.169.254", "8.8.8.8", "808:808"]) {
    const origin = `http://[::ffff:${address}]:7575`;
    assert.equal(ssoCallbackPreview(origin), null, address);
  }
});


test("IPv6 previews use the Host spelling sent by browsers for callback registration", () => {
 for (const [input,expected] of [
  ["http://[::ffff:192.168.1.20]:7575", "http://[::ffff:c0a8:114]:7575"],
  ["http://[::ffff:10.0.0.2]:7575", "http://[::ffff:a00:2]:7575"],
  ["http://[FD00:0000:0000:0000:0000:0000:0000:0001]:7575", "http://[fd00::1]:7575"],
  ["https://[2001:0DB8:0000:0000:0000:0000:0000:0001]:8443", "https://[2001:db8::1]:8443"],
  ["https://[2001:db8:0:0:0:0:0:1]:443/", "https://[2001:db8::1]"],
 ]) {
  assert.equal(ssoCallbackPreview(input), `${expected}/api/auth/sso/callback`,input);
  assert.equal(ssoCallbackPreview(expected), `${expected}/api/auth/sso/callback`,expected);
 }
});
