import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import ts from "typescript";

const source = await readFile(new URL("../src/components/devices/DeviceUssdTab.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText;

class ApiError extends Error {
  constructor(code, message) { super(message); this.code = code; }
}

// Render the component with deterministic hooks and an intercepted API. This
// exercises its event handlers without a browser, modem or extra dependencies.
function terminal(responses) {
  const state = [];
  const requests = [];
  let cursor = 0;
  const element = (type, props) => ({ type, props });
  const modules = {
    react: { useState(initial) {
      const index = cursor++;
      if (!(index in state)) state[index] = initial;
      return [state[index], (value) => { state[index] = typeof value === "function" ? value(state[index]) : value; }];
    } },
    "react/jsx-runtime": { jsx: element, jsxs: element },
    "@fluentui/react-icons": { CallRegular: "Icon" },
    "../../api": { ApiError, api: async (path, options) => {
      requests.push({ path, ...options });
      assert.ok(responses.length, "unexpected extra network submission");
      const response = responses.shift();
      if (response instanceof Error) throw response;
      return response;
    } },
    "../ui": { Button: "Button", Input: "Input" },
    "./UssdLogEntry": { UssdLogEntry: "Log" },
    "./AtLogEntry": { AtTypingBubble: "Waiting" },
    "../../lib/i18n": { useI18n: () => ({ t: (s) => s }), tf: (s) => s },
  };
  const exports = {};
  new Function("require", "exports", compiled)((name) => {
    assert.ok(name in modules, `unexpected dependency ${name}`);
    return modules[name];
  }, exports);
  function nodes() {
    cursor = 0;
    const found = [];
    function visit(node) {
      if (Array.isArray(node)) { node.forEach(visit); return; }
      if (!node || typeof node !== "object") return;
      found.push(node);
      visit(node.props?.children);
    }
    visit(exports.DeviceUssdTab({ deviceId: "saved" }));
    return found;
  }
  function button(label) { return nodes().find((n) => n.type === "Button" && n.props.children === label); }
  return {
    requests,
    button,
    logs: () => nodes().filter((n) => n.type === "Log").map((n) => n.props.item),
    async send(value) {
      const input = nodes().find((n) => n.type === "Input" && !n.props.type);
      input.props.onChange({ target: { value } });
      const send = button("回复") || button("发送");
      await send.props.onClick();
    },
    async cancel() { await button("取消会话").props.onClick(); },
  };
}

const menu = { result: { status: "awaiting_input", text: "1. Balance", continueable: true }, sessionId: "ussi-token" };

test("USSD reads the API's top-level session ID and replies to that session", async () => {
  const ui = terminal([menu, { result: { status: "final", text: "Balance" }, sessionId: "" }]);
  await ui.send("#999#");
  assert.ok(ui.button("回复"));
  await ui.send("1");
  assert.deepEqual(ui.requests.map((r) => r.path), ["/devices/saved/actions/ussd", "/devices/saved/actions/ussd/continue"]);
  assert.equal(ui.requests[1].body.sessionId, "ussi-token");
  assert.equal(ui.requests[1].body.input, "1");
  assert.equal(ui.button("取消会话"), undefined);
});

test("failed cancellation displays the error and preserves the binding for retry", async () => {
  const ui = terminal([menu, new ApiError("modem_error", "BYE failed"), { cancelled: true }]);
  await ui.send("#999#");
  await ui.cancel();
  assert.ok(ui.button("取消会话"));
  assert.ok(ui.logs().some((l) => l.type === "err" && l.content === "BYE failed"));
  assert.ok(!ui.logs().some((l) => l.content === "会话已手动取消"));
  await ui.cancel();
  assert.deepEqual(ui.requests.slice(1).map((r) => r.body), [{ sessionId: "ussi-token" }, { sessionId: "ussi-token" }]);
  assert.equal(ui.button("取消会话"), undefined);
});

test("an uncertain menu reply keeps its session for explicit cancellation", async () => {
  const ui = terminal([menu, new ApiError("modem_timeout", "Timed out"), { cancelled: true }]);
  await ui.send("#999#");
  await ui.send("1");
  assert.ok(ui.button("取消会话"));
  assert.equal(ui.requests.length, 2);
  await ui.cancel();
  assert.equal(ui.requests[2].path, "/devices/saved/actions/ussd/cancel");
  assert.equal(ui.requests[2].body.sessionId, "ussi-token");
});

test("a definitively closed session is cleared and raw responses are displayed", async () => {
  const ui = terminal([menu, new ApiError("ussd_session_not_found", "Closed"), { result: { status: "final", raw: "raw response" } }]);
  await ui.send("#999#");
  await ui.cancel();
  assert.equal(ui.button("取消会话"), undefined);
  await ui.send("#999#");
  assert.ok(ui.logs().some((l) => l.content === "raw response"));
});
