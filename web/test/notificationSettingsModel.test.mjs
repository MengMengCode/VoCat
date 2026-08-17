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
  buildLarkPayload,
  buildNotificationsPayload,
  formsFromNotifications,
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
    enabled: false,
    signing_enabled: false,
    payload_template: DEFAULT_LARK_PAYLOAD_TEMPLATE,
  });
});
