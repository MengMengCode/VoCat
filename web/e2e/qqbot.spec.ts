import { expect, test, type Page } from "@playwright/test";

type Binding = {
  id: string;
  mode: "code";
  code: string;
  expires_at: string;
  status: "waiting" | "bound" | "canceled";
};

function binding(id: string, status: Binding["status"] = "waiting"): Binding {
  return { id, mode: "code", code: `VOCAT-${id}`, expires_at: new Date(Date.now() + 300_000).toISOString(), status };
}

const credentials = { enabled: false, app_id: "102000001", app_secret: "********", target_openid: "", proxy: "" };

async function mockSettings(page: Page, options: {
  initial: Binding | null;
  beforeRead?: (attempt: number) => Promise<boolean>;
  cancel?: (id: string) => Promise<{ status: number; data?: Binding }>;
  notifications?: (attempt: number) => typeof credentials | Promise<typeof credentials>;
}) {
  await page.context().addInitScript(() => localStorage.setItem("vocat_disclaimer_agreed_at", String(Date.now())));
  let current = options.initial;
  const calls = { reads: 0, starts: 0, cancels: [] as string[], settings: 0 };
  await page.route("**/api/**", async route => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    let data: unknown = {};
    let status = 200;
    if (path === "/api/auth/session") data = { authenticated: true, username: "admin", role: "Administrator", csrf_token: "mock-csrf" };
    else if (path === "/api/settings/preferences") data = { language: "zh" };
    else if (path === "/api/settings/notifications/qqbot/bind") {
      if (request.method() === "GET") {
        calls.reads++;
        if (options.beforeRead && !await options.beforeRead(calls.reads)) status = 503;
        data = current;
      } else if (request.method() === "POST") {
        calls.starts++;
        current = binding(`new-${calls.starts}`);
        data = current;
      } else {
        const id = url.searchParams.get("id") || "";
        calls.cancels.push(id);
        const result = await options.cancel?.(id);
        status = result?.status ?? 200;
        if (status === 200) {
          if (current) current = { ...current, status: "canceled" };
          data = { status: "canceled" };
        } else if (result?.data) current = result.data;
      }
    } else if (path === "/api/settings/notifications") {
      calls.settings++;
      data = { qqbot: options.notifications ? await options.notifications(calls.settings) : credentials };
    } else if (path === "/api/devices") data = { devices: [] };
    else if (path === "/api/extensions") data = [];
    else if (path === "/api/settings/tokens") data = { tokens: [] };
    else if (path === "/api/settings/security") data = { mode: "internal", allowed_cidrs: [], trust_proxy_headers: false };
    else if (path === "/api/settings/sso") status = 503;
    await route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(status === 200 ? { data } : { error: { code: status === 409 ? "qqbot_bind_changed" : "unavailable", message: "Retry the binding state" } }),
    });
  });
  return calls;
}

async function openQQSettings(page: Page) {
  await page.goto("/settings");
  await page.getByRole("button", { name: "QQ 机器人", exact: true }).click();
}

test("restores an existing binding after a state failure before allowing automatic QR or saves", async ({ page }) => {
  let release: () => void = () => {};
  const waiting = new Promise<void>(resolve => { release = resolve; });
  const calls = await mockSettings(page, {
    initial: binding("existing"),
    beforeRead: async attempt => {
      if (attempt === 1) return false;
      await waiting;
      return true;
    },
  });
  await page.goto("/settings");
  await expect.poll(() => calls.reads).toBeGreaterThanOrEqual(2);
  await expect(page.getByRole("button", { name: "保存通知配置", exact: true })).toBeDisabled();
  await expect(page.getByText("正在加载通知配置…", { exact: true })).toBeVisible();
  expect(calls.starts).toBe(0);
  release();
  await page.getByRole("button", { name: "QQ 机器人", exact: true }).click();
  await expect(page.getByText("VOCAT-existing", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "取消", exact: true })).toBeEnabled();
  expect(calls.starts).toBe(0);
});

test("waits for cancellation before allowing a replacement binding", async ({ page }) => {
  let release: () => void = () => {};
  const waiting = new Promise<void>(resolve => { release = resolve; });
  const calls = await mockSettings(page, {
    initial: binding("existing"),
    cancel: async () => { await waiting; return { status: 200 }; },
  });
  await openQQSettings(page);
  await expect(page.getByText("VOCAT-existing", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect.poll(() => calls.cancels).toEqual(["existing"]);
  const start = page.getByRole("button", { name: "获取绑定码", exact: true });
  await expect(start).toBeDisabled();
  await expect(page.getByRole("button", { name: "保存通知配置", exact: true })).toBeDisabled();
  expect(calls.starts).toBe(0);
  release();
  await expect(start).toBeEnabled();
  await start.click();
  await expect(page.getByText("VOCAT-new-1", { exact: true })).toBeVisible();
  expect(calls.starts).toBe(1);
});

test("restores a newer binding when cancellation targets an older session", async ({ page }) => {
  const calls = await mockSettings(page, {
    initial: binding("existing"),
    cancel: async id => {
      expect(id).toBe("existing");
      return { status: 409, data: binding("other-tab") };
    },
  });
  await openQQSettings(page);
  await expect(page.getByText("VOCAT-existing", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "取消", exact: true }).click();
  await expect(page.getByText("VOCAT-other-tab", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "取消", exact: true })).toBeEnabled();
  expect(calls.starts).toBe(0);
});

test("reloads committed credentials when binding finished during page loading", async ({ page }) => {
  const saved = { ...credentials, enabled: true, app_id: "102000002", target_openid: "NEW_RECIPIENT" };
  let release: () => void = () => {};
  const waiting = new Promise<void>(resolve => { release = resolve; });
  const calls = await mockSettings(page, {
    initial: binding("completed", "bound"),
    notifications: async attempt => {
      if (attempt === 1) return { ...credentials, app_id: "", app_secret: "" };
      await waiting;
      return saved;
    },
  });
  await openQQSettings(page);
  await expect.poll(() => calls.settings).toBeGreaterThanOrEqual(2);
  await expect(page.getByRole("button", { name: "生成二维码", exact: true })).toBeDisabled();
  expect(calls.starts).toBe(0);
  release();
  await expect(page.getByLabel("机器人 AppID", { exact: true })).toHaveValue(saved.app_id);
  await expect(page.getByLabel("接收方 openid（QQ 单聊用户）", { exact: true })).toHaveValue(saved.target_openid);
  await expect(page.getByRole("button", { name: "保存通知配置", exact: true })).toBeEnabled();
  expect(calls.settings).toBeGreaterThanOrEqual(2);
  expect(calls.starts).toBe(0);
  // Editing one field stays in maintenance; clearing the complete draft permits rebinding.
  await page.getByLabel("机器人 AppID", { exact: true }).fill("");
  await expect(page.getByLabel("接收方 openid（QQ 单聊用户）", { exact: true })).toBeVisible();
  expect(calls.starts).toBe(0);
  await page.getByRole("button", { name: "清空配置重新绑定", exact: true }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "清空并重新绑定", exact: true }).click();
  await expect.poll(() => calls.starts).toBe(1);
});
