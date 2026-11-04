import { expect, test, type BrowserContext, type Page } from "@playwright/test";

const APP = "https://vocat.test:17475";
const IDP = "https://pocket.test:17476";
const FIXTURE = "https://127.0.0.1:17475";
const PASSWORD = "browser-test-local-password";
const SECRET = "browser-test-client-secret";

/** Accept the first-run disclaimer in each browser context so tests exercise authentication directly. */
async function prepare(context: BrowserContext) {
  await context.addInitScript(() => localStorage.setItem("vocat_disclaimer_agreed_at", String(Date.now())));
}

/** Sign in through the real local login form and check the complete return destination. */
async function passwordLogin(page: Page, target = "/settings") {
  await page.goto(`/login?${new URLSearchParams({ redirect: target })}`);
  await page.locator('input[autocomplete="username"]').fill("admin");
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await expect(page).toHaveURL(APP + target);
}

/** Locate the accessible SSO settings region independently of surrounding settings cards. */
function card(page: Page) { return page.getByRole("region", { name: "Single sign-on (SSO)", exact: true }); }

/** Wait for the saved provider settings to finish loading. */
async function settings(page: Page) {
  await expect(card(page).getByLabel("Identity provider", { exact: true })).toHaveValue("pocketid");
}

/** Save the SSO form and inspect its response unless revocation immediately replaces the document. */
async function save(page: Page, navigates = false) {
  const response = page.waitForResponse((response) => response.url() === APP + "/api/settings/sso" && response.request().method() === "PUT");
  await card(page).getByRole("button", { name: "Save SSO settings", exact: true }).click();
  const result = await response;
  expect(result.status()).toBe(200);
  // A successful revocation starts a document navigation immediately; Chromium
  // then releases the prior document's response bodies. Verify its visible
  // success and cookies below instead of reading a released CDP resource.
  return navigates ? undefined : (await result.json()).data;
}

/** Complete authorization through the separate fixture identity-provider site. */
async function approve(page: Page) {
  await expect(page).toHaveURL(new RegExp(`^${IDP.replaceAll(".", "\\.")}/authorize\\?`));
  await page.getByRole("button", { name: "Authorize test account", exact: true }).click();
}

/** Inspect the current browser session using its real application cookies. */
async function checkSession(page: Page) {
  return page.evaluate(async () => {
    const response = await fetch("/api/auth/session", { credentials: "include" });
    return { status: response.status, body: await response.json() };
  });
}

test.beforeEach(async ({ context, request }) => {
  const reset = await request.post(FIXTURE + "/__e2e/reset", { headers: { "X-VoCat-E2E": "fixture" } });
  expect(reset.status()).toBe(204);
  await prepare(context);
});

test("password login preserves the full target and rejects unsafe logged-in redirects", async ({ page }) => {
  await passwordLogin(page, "/settings?tab=security#sso");
  await settings(page);
  await expect(card(page).getByLabel("VoCat public origin", { exact: true })).toHaveValue(APP);
  await expect(card(page).getByRole("switch", { name: "Enable SSO sign-in", exact: true })).toBeDisabled();
  for (const redirect of ["https://evil.example/", "//evil.example/", "/%252f%252fevil.example/", "/login?redirect=/settings", "/%6cogin?redirect=/settings", "/api/%61uth/sso/start", "/api/auth/sso/start"]) {
    await page.goto(`/login?${new URLSearchParams({ redirect })}`);
    await expect(page).toHaveURL(APP + "/");
  }
});

test("callback URL is available before creating the OIDC client and follows unsaved origin edits", async ({ page, context }) => {
  await passwordLogin(page);
  await settings(page);
  await expect(card(page).getByText(APP + "/api/auth/sso/callback", { exact: true })).toBeVisible();
  const copy = card(page).getByRole("button", { name: "Copy", exact: true });
  await expect(copy).toBeEnabled();
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: APP });
  await copy.click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(APP + "/api/auth/sso/callback");
  await expect(card(page).getByLabel("Client ID", { exact: true })).toHaveValue("");
  await expect(card(page).getByPlaceholder("Enter a new secret", { exact: true })).toHaveValue("");
  await expect(card(page).getByText("Enter the Pocket ID instance URL", { exact: true })).toBeVisible();
  await expect(card(page).getByText("Enter the client ID created for VoCat in your identity service", { exact: true })).toBeVisible();

  const origin = card(page).getByLabel("VoCat public origin", { exact: true });
  await origin.fill("https://vocat.nas.test:8443");
  await expect(card(page).getByText("https://vocat.nas.test:8443/api/auth/sso/callback", { exact: true })).toBeVisible();
  await expect(copy).toBeEnabled();
  await copy.click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("https://vocat.nas.test:8443/api/auth/sso/callback");
  const persisted = await page.evaluate(async () => (await (await fetch("/api/settings/sso", { credentials: "include" })).json()).data.config);
  expect(persisted.revision).toBe(0);
  expect(persisted.public_origin).toBe("");

  for (const [input, normalized] of [
    ["http://[::ffff:192.168.1.20]:7575", "http://[::ffff:c0a8:114]:7575"],
    ["http://[0:0:0:0:0:0:0:1]:7575", "http://[::1]:7575"],
  ]) {
    await origin.fill(input);
    await expect(card(page).getByText(normalized + "/api/auth/sso/callback", { exact: true })).toBeVisible();
    await copy.click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(normalized + "/api/auth/sso/callback");
  }

  await origin.fill("https://vocat.nas.test:8443/with-a-path");
  await expect(copy).toBeDisabled();
  await expect(card(page).getByText("https://vocat.nas.test:8443/api/auth/sso/callback", { exact: true })).toHaveCount(0);
});

test("cross-site Pocket ID binding, cancellation, SSO login and revocation keep cookie and CSRF boundaries", async ({ page, context, browser, request }) => {
  await passwordLogin(page);
  await settings(page);
  const originalSession = (await context.cookies(APP)).find((cookie) => cookie.name === "vocat_session");
  expect(originalSession?.sameSite).toBe("Strict");
  expect(originalSession?.secure).toBe(true);
  expect(originalSession?.httpOnly).toBe(true);

  await card(page).getByLabel("Pocket ID Issuer", { exact: true }).fill(IDP);
  await card(page).getByLabel("Client ID", { exact: true }).fill("vocat-browser-test");
  await card(page).getByPlaceholder("Enter a new secret", { exact: true }).fill(SECRET);
  await expect(card(page).getByRole("button", { name: "Check settings", exact: true })).toBeDisabled();
  const configured = await save(page);
  expect(configured.config.enabled).toBe(false);
  expect(configured.config.token_auth_method).toBe("auto");
  expect(configured.has_client_secret).toBe(true);
  expect(configured.configured).toBe(true);
  expect(Object.keys(configured).sort()).toEqual(["binding", "config", "configured", "has_client_secret", "reauthentication_required"]);
  expect(Object.hasOwn(configured.config, "provider_instance_id")).toBe(false);
  expect(JSON.stringify(configured)).not.toContain(SECRET);
  await expect(card(page).getByPlaceholder("Enter a new secret", { exact: true })).toHaveValue("");
  await expect(card(page).getByText(APP + "/api/auth/sso/callback", { exact: true })).toBeVisible();
  await expect(card(page).getByRole("checkbox")).toHaveCount(0);

  const checked = page.waitForResponse(APP + "/api/settings/sso/check");
  await card(page).getByRole("button", { name: "Check settings", exact: true }).click();
  const check = await checked;
  expect(check.status()).toBe(200);
  const checkBody = await check.json();
  expect(checkBody.data).toEqual({ revision: configured.config.revision });
  await expect(card(page).getByRole("status")).toHaveText("Configuration check passed");

  // A real navigation to a different site removes Strict main cookies from
  // the top-level callback. A separate Lax cookie proves browser association.
  const parallelTab = await context.newPage();
  await parallelTab.goto(APP + "/settings");
  await settings(parallelTab);
  const bindingRequest = page.waitForRequest((request) => request.url() === APP + "/api/auth/sso/bind" && request.method() === "POST");
  await card(page).getByRole("button", { name: "Bind external account", exact: true }).click();
  expect((await bindingRequest).postDataJSON()).toEqual({ expected_revision: configured.config.revision });
  await card(parallelTab).getByRole("button", { name: "Bind external account", exact: true }).click();
  const pendingCookies = (await context.cookies(APP + "/api/auth/sso/callback")).filter((cookie) => cookie.name.startsWith("vocat_sso_"));
  expect(pendingCookies).toHaveLength(2);
  expect(pendingCookies.every((cookie) => cookie.sameSite === "Lax" && cookie.httpOnly && cookie.secure && cookie.path === "/api/auth/sso")).toBe(true);
  await page.getByRole("button", { name: "Cancel authorization", exact: true }).click();
  await expect(page).toHaveURL(APP + "/settings");
  await expect(card(page).getByRole("alert")).toHaveText("Authentication was cancelled. You can try again.");
  expect((await context.cookies(APP)).find((cookie) => cookie.name === "vocat_session")?.value).toBe(originalSession?.value);

  // Completing the other tab still works after the first flow was cancelled.
  await approve(parallelTab);
  await expect(parallelTab).toHaveURL(APP + "/settings");
  await page.goto(APP + "/settings");
  await expect(page).toHaveURL(APP + "/settings");
  await expect(card(page).getByText("Browser test administrator", { exact: true })).toBeVisible();
  for (const name of ["Bind external account", "Verify existing binding", "Replace account"]) {
    await expect(card(page).getByRole("button", { name, exact: true })).toHaveCount(0);
  }
  await expect(card(page).getByRole("button", { name: "Unbind account", exact: true })).toBeVisible();
  expect((await context.cookies(APP)).find((cookie) => cookie.name === "vocat_session")?.value).toBe(originalSession?.value);

  const statsResponse = await request.get(FIXTURE + "/__e2e/stats", { headers: { "X-VoCat-E2E": "fixture" } });
  const stats = await statsResponse.json();
  expect(stats.callbacks).toHaveLength(2);
  for (const callback of stats.callbacks) {
    expect(callback.status).toBe(303);
    expect(callback.cookies.some((name: string) => name.startsWith("vocat_sso_"))).toBe(true);
    expect(callback.cookies).not.toContain("vocat_session");
    expect(callback.cookies).not.toContain("vocat_csrf");
    expect(callback.cleared_cookies).not.toContain("vocat_session");
    expect(callback.cleared_cookies).not.toContain("vocat_csrf");
  }
  expect(stats.pkce_verified).toBe(true);
  expect(stats.basic_verified).toBe(true);
  expect(stats.redirect_verified).toBe(true);
  expect(stats.token_requests).toBe(1);
  await parallelTab.close();

  await card(page).getByRole("switch", { name: "Enable SSO sign-in", exact: true }).click();
  const enabled = await save(page);
  expect(enabled.config.enabled).toBe(true);
  expect(enabled.reauthentication_required).toBe(false);
  // The SPA scrolls inside a fixed-height shell. Give the whole card enough
  // vertical room for an element capture while retaining each responsive width.
  await page.setViewportSize({ width: 1280, height: 1800 });
  await card(page).screenshot({ path: test.info().outputPath("sso-settings-desktop.png") });
  await page.setViewportSize({ width: 390, height: 2200 });
  await card(page).screenshot({ path: test.info().outputPath("sso-settings-mobile.png") });

  const anonymous = await browser.newContext({ ignoreHTTPSErrors: true });
  await prepare(anonymous);
  const loginPage = await anonymous.newPage();
  const target = "/sms?keyword=%E6%B5%8B%E8%AF%95#latest";
  await loginPage.goto(APP + `/login?${new URLSearchParams({ redirect: target })}`);
  await loginPage.getByRole("link", { name: "Continue with Pocket ID", exact: true }).click();
  await loginPage.getByRole("button", { name: "Cancel authorization", exact: true }).click();
  await expect(loginPage).toHaveURL(APP + `/login?${new URLSearchParams({ redirect: target })}`);
  await expect(loginPage.getByRole("alert")).toHaveText("Authentication was cancelled. You can try again.");
  await expect(loginPage.getByRole("link", { name: "Continue with Pocket ID", exact: true })).toBeVisible();

  // Manual retry creates a fresh flow; no automatic authorization loop.
  await loginPage.getByRole("link", { name: "Continue with Pocket ID", exact: true }).click();
  await approve(loginPage);
  await expect(loginPage).toHaveURL(APP + target);
  await expect.poll(async () => loginPage.evaluate(() => !!sessionStorage.getItem("vocat.csrf"))).toBe(true);
  const session = await checkSession(loginPage);
  expect(session.status).toBe(200);
  expect(session.body.data.authenticated).toBe(true);
  const firstWrite = await loginPage.evaluate(async () => {
    const response = await fetch("/api/settings/preferences", {
      method: "PUT", credentials: "include",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": sessionStorage.getItem("vocat.csrf") || "" },
      body: JSON.stringify({ language: "en" }),
    });
    return response.status;
  });
  expect(firstWrite).toBe(200);
  const ssoSession = (await anonymous.cookies(APP)).find((cookie) => cookie.name === "vocat_session");
  expect(ssoSession?.value).not.toBe(originalSession?.value);
  expect(ssoSession?.sameSite).toBe("Strict");
  const storage = await loginPage.evaluate(() => JSON.stringify({ local: { ...localStorage }, session: { ...sessionStorage } }));
  expect(storage).not.toContain("browser-test-must-never-persist");
  expect(storage).not.toContain("browser-test-transient-access-token");

  // Saving from an SSO session is a successful operation that explicitly
  // revokes that session. The UI must display success before reauthentication.
  await loginPage.goto(APP + "/settings");
  await settings(loginPage);
  await card(loginPage).getByLabel("Sign-in button label", { exact: true }).fill("Browser SSO");
  await save(loginPage, true);
  await expect(loginPage).toHaveURL(APP + "/login?redirect=%2Fsettings");
  await expect(loginPage.getByRole("status").filter({ hasText: "SSO settings saved. Sign in again." })).toBeVisible();
  expect((await anonymous.cookies(APP)).find((cookie) => cookie.name === "vocat_session")).toBeUndefined();
  expect((await checkSession(page)).status).toBe(200);

  await loginPage.getByRole("link", { name: "Continue with Browser SSO", exact: true }).click();
  await approve(loginPage);
  await expect(loginPage).toHaveURL(APP + "/settings");
  await settings(loginPage);
  await card(loginPage).getByRole("button", { name: "Unbind account", exact: true }).click();
  const unboundResponse = loginPage.waitForResponse((response) => response.url() === APP + "/api/auth/sso/binding" && response.request().method() === "DELETE");
  await loginPage.getByRole("alertdialog").getByRole("button", { name: "Unbind account", exact: true }).click();
  const unbound = await unboundResponse;
  expect(unbound.status()).toBe(200);
  await expect(loginPage).toHaveURL(APP + "/login?redirect=%2Fsettings");
  await expect(loginPage.getByRole("status").filter({ hasText: "External account unbound and SSO sign-in disabled." })).toBeVisible();
  await expect(loginPage.getByRole("link", { name: "Continue with Browser SSO", exact: true })).toHaveCount(0);
  expect((await checkSession(page)).status).toBe(200);
  await anonymous.close();
});

test("expanded settings preserve saved origins and expose protocol choices only for Generic OAuth2", async ({ page }) => {
  await passwordLogin(page);
  await settings(page);
  const canonicalOrigin = "https://vocat.nas.test";
  const seeded = await page.evaluate(async ({ appOrigin, idpOrigin, secret }) => {
    const session = await (await fetch("/api/auth/session", { credentials: "include" })).json();
    const response = await fetch("/api/settings/sso", {
      method: "PUT", credentials: "include",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": session.data.csrf_token },
      body: JSON.stringify({
        expected_revision: 0, client_secret: secret,
        config: {
          kind: "pocketid", enabled: false, display_name: "NAS sign-in", public_origin: appOrigin,
          issuer_url: idpOrigin, client_id: "vocat-browser-test", scopes: ["openid", "email"],
          token_auth_method: "client_secret_post", pkce_mode: "s256",
        },
      }),
    });
    return response.status;
  }, { appOrigin: canonicalOrigin, idpOrigin: IDP, secret: SECRET });
  expect(seeded).toBe(200);
  await page.reload();
  await settings(page);

  // Existing callbacks and protocol choices survive a form simplification,
  // including a canonical origin different from the current browser origin.
  await expect(card(page).getByLabel("VoCat public origin", { exact: true })).toHaveValue(canonicalOrigin);
  await expect(card(page).getByLabel("VoCat public origin", { exact: true })).toBeVisible();
  await expect(card(page).getByLabel("Sign-in button label", { exact: true })).toBeVisible();
  await expect(card(page).locator("details")).toHaveCount(0);
  await expect(card(page).getByLabel("Identity service connection", { exact: true })).toHaveCount(0);
  await expect(card(page).getByLabel("Other backend addresses", { exact: true })).toHaveCount(0);
  await expect(card(page).getByText(canonicalOrigin + "/api/auth/sso/callback", { exact: true })).toBeVisible();
  await expect(card(page).getByLabel("Token client authentication", { exact: true })).toHaveCount(0);
  await expect(card(page).getByLabel("PKCE", { exact: true })).toHaveCount(0);
  await expect(card(page).getByLabel("Scopes (space separated)", { exact: true })).toHaveCount(0);

  await card(page).getByLabel("Sign-in button label", { exact: true }).fill("My NAS sign-in");
  const saved = await save(page);
  expect(saved.config).toMatchObject({
    public_origin: canonicalOrigin, display_name: "My NAS sign-in", scopes: ["openid", "email"],
    token_auth_method: "client_secret_post", pkce_mode: "s256",
  });
  expect(saved.has_client_secret).toBe(true);
  for (const obsolete of ["allowed_backend_origins", "allow_private_network", "allow_insecure_http", "provider_instance_id"]) {
    expect(Object.hasOwn(saved.config, obsolete)).toBe(false);
  }
  expect(Object.hasOwn(saved, "callback_url")).toBe(false);

  await card(page).getByLabel("Client ID", { exact: true }).fill("different-client");
  await card(page).getByRole("button", { name: "Save SSO settings", exact: true }).click();
  await expect(card(page).getByRole("alert")).toHaveText("The identity source changed. Enter a new client secret.");
  const unchanged = await page.evaluate(async () => (await (await fetch("/api/settings/sso", { credentials: "include" })).json()).data);
  expect(unchanged.config.revision).toBe(saved.config.revision);
  expect(unchanged.has_client_secret).toBe(true);

  await card(page).getByLabel("Identity provider", { exact: true }).selectOption("generic");
  await expect(card(page).getByLabel("Token client authentication", { exact: true })).toBeVisible();
  await expect(card(page).getByLabel("PKCE", { exact: true })).toBeVisible();
  await card(page).getByLabel("Identity provider", { exact: true }).selectOption("github");
  await expect(card(page).getByLabel("Token client authentication", { exact: true })).toHaveCount(0);
  await expect(card(page).getByLabel("PKCE", { exact: true })).toHaveCount(0);
  await expect(card(page).getByLabel("Identity service connection", { exact: true })).toHaveCount(0);
});

test("provider metadata recovers after a transient failure and stops retrying after success", async ({ page }) => {
  let attempts = 0;
  await page.route("**/api/auth/providers", (route) => {
    attempts++;
    return attempts === 1 ? route.abort() : route.fulfill({
      json: { data: { sso: { enabled: true, display_name: "Recovered provider" } } },
    });
  });
  await page.clock.install();
  await page.goto("/login");
  await expect.poll(() => attempts).toBe(1);
  await page.clock.runFor(1100);
  await expect(page.getByRole("link", { name: "Continue with Recovered provider", exact: true })).toBeVisible();
  await page.clock.fastForward(30_000);
  expect(attempts).toBe(2);
});

test("provider metadata retries are bounded while password sign-in stays available", async ({ page }) => {
  let attempts = 0;
  await page.route("**/api/auth/providers", (route) => {
    attempts++;
    return route.abort();
  });
  await page.clock.install();
  await page.goto("/login");
  await expect.poll(() => attempts).toBe(1);
  for (const [index, delay] of [1000, 2000, 4000].entries()) {
    await page.clock.runFor(delay + 100);
    await expect.poll(() => attempts).toBe(index + 2);
  }
  await page.clock.fastForward(30_000);
  expect(attempts).toBe(4);
  await expect(page.locator('input[autocomplete="current-password"]')).toBeEnabled();
  await page.locator('input[autocomplete="username"]').fill("admin");
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await expect(page).toHaveURL(APP + "/");
});

test("provider metadata failures leave password sign-in usable and cancel retries after navigation", async ({ page }) => {
  let attempts = 0;
  await page.route("**/api/auth/providers", (route) => {
    attempts++;
    return route.abort();
  });
  await page.clock.install();
  await passwordLogin(page);
  await settings(page);
  const attemptsAfterLogin = attempts;
  await page.clock.fastForward(30_000);
  expect(attempts).toBe(attemptsAfterLogin);
});

test("one saved HTTPS provider serves local HTTP and HTTPS browser logins automatically", async ({ page, browser }) => {
  await passwordLogin(page);
  await settings(page);
  await card(page).getByLabel("Pocket ID Issuer", { exact: true }).fill(IDP);
  await card(page).getByLabel("Client ID", { exact: true }).fill("vocat-browser-test");
  await card(page).getByPlaceholder("Enter a new secret", { exact: true }).fill(SECRET);
  await save(page);
  await card(page).getByRole("button", { name: "Bind external account", exact: true }).click();
  await approve(page);
  await expect(page).toHaveURL(APP + "/settings");
  await expect(card(page).getByText("Browser test administrator", { exact: true })).toBeVisible();
  await settings(page);
  await card(page).getByRole("switch", { name: "Enable SSO sign-in", exact: true }).click();
  await save(page);
  for (const origin of ["http://127.0.0.1:17477", "http://localhost:17477", APP]) {
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    await prepare(context);
    const p = await context.newPage();
    await p.goto(origin + "/login");
    await p.getByRole("link", { name: "Continue with Pocket ID", exact: true }).click();
    expect(new URL(p.url()).searchParams.get("redirect_uri")).toBe(origin + "/api/auth/sso/callback");
    await approve(p);
    await expect(p).toHaveURL(origin + "/");
    const session = await checkSession(p);
    expect(session.status).toBe(200);
    const cookies = await context.cookies(origin);
    for (const name of ["vocat_session", "vocat_csrf"]) {
      const cookie = cookies.find(c => c.name === name);
      expect(cookie?.secure).toBe(origin.startsWith("https:"));
      expect(cookie?.sameSite).toBe("Strict");
    }
    const result = await p.evaluate(async () => {
      const session = await (await fetch("/api/auth/session")).json();
      const response = await fetch("/api/settings/security", { method: "PUT", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.data.csrf_token }, body: JSON.stringify({ mode: "internal" }) });
      return response.status;
    });
    expect(result).toBe(200);
    const saved = await p.evaluate(async () => (await (await fetch("/api/settings/sso")).json()).data.config);
    expect(saved.public_origin).toBe(APP);
    await context.close();
    const passwordContext = await browser.newContext({ ignoreHTTPSErrors: true });
    await prepare(passwordContext);
    const passwordPage = await passwordContext.newPage();
    await passwordPage.goto(origin + "/login?redirect=%2Fsettings");
    await passwordPage.locator('input[autocomplete="username"]').fill("admin");
    await passwordPage.locator('input[autocomplete="current-password"]').fill(PASSWORD);
    await passwordPage.getByRole("button", { name: "Sign In", exact: true }).click();
    await expect(passwordPage).toHaveURL(origin + "/settings");
    expect((await passwordContext.cookies(origin)).find(c => c.name === "vocat_session")?.secure).toBe(origin.startsWith("https:"));
    expect((await checkSession(passwordPage)).status).toBe(200);
    await passwordContext.close();
  }
});

test("a saved HTTPS SSO draft does not change an existing HTTP password client", async ({ page, browser }) => {
  await passwordLogin(page);
  await settings(page);
  await card(page).getByLabel("VoCat public origin", { exact: true }).fill("https://vocat.test:17477");
  await card(page).getByLabel("Pocket ID Issuer", { exact: true }).fill(IDP);
  await card(page).getByLabel("Client ID", { exact: true }).fill("vocat-browser-test");
  await card(page).getByPlaceholder("Enter a new secret", { exact: true }).fill(SECRET);
  const saved = await save(page);
  expect(saved.config.enabled).toBe(false);

  const context = await browser.newContext();
  await prepare(context);
  try {
    const p = await context.newPage();
    const origin = "http://vocat.test:17477";
    await p.goto(origin + "/login?redirect=%2Fsettings");
    await p.locator('input[autocomplete="username"]').fill("admin");
    await p.locator('input[autocomplete="current-password"]').fill(PASSWORD);
    await p.getByRole("button", { name: "Sign In", exact: true }).click();
    await expect(p).toHaveURL(origin + "/settings");
    expect((await checkSession(p)).status).toBe(200);
    for (const name of ["vocat_session", "vocat_csrf"]) {
      expect((await context.cookies(origin)).find(c => c.name === name)?.secure).toBe(false);
    }
    const statuses = await p.evaluate(async () => {
      const session = await (await fetch("/api/auth/session")).json();
      const headers = { "Content-Type": "application/json", "X-CSRF-Token": session.data.csrf_token };
      const write = await fetch("/api/settings/security", { method: "PUT", headers, body: JSON.stringify({ mode: "internal" }) });
      const logout = await fetch("/api/auth/logout", { method: "POST", headers });
      const afterLogout = await fetch("/api/auth/session");
      return [write.status, logout.status, afterLogout.status];
    });
    expect(statuses).toEqual([200, 200, 401]);
  } finally {
    await context.close();
  }
});

test("HTTPS proxy password cookies stay Secure without enabling proxy-header trust", async ({ page, context }) => {
  // The TLS fixture strips TLS state before handing requests to the real app.
  await passwordLogin(page);
  for (const name of ["vocat_session", "vocat_csrf"]) {
    expect((await context.cookies(APP)).find(c => c.name === name)?.secure).toBe(true);
  }
  await context.clearCookies({ name: "vocat_csrf" });
  expect((await context.cookies(APP)).some(c => c.name === "vocat_csrf")).toBe(false);
  const statuses = await page.evaluate(async () => {
    const response = await fetch("/api/auth/session", { referrerPolicy: "no-referrer" });
    const session = await response.json();
    const write = await fetch("/api/settings/security", { method: "PUT", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.data.csrf_token }, body: JSON.stringify({ mode: "internal" }) });
    return [response.status, write.status];
  });
  expect(statuses).toEqual([200, 200]);
  for (const name of ["vocat_session", "vocat_csrf"]) {
    expect((await context.cookies(APP)).find(c => c.name === name)?.secure).toBe(true);
  }
  await page.goto("http://vocat.test:17477/login");
  expect((await checkSession(page)).status).toBe(401);
  await page.goto(APP + "/settings");
  expect((await checkSession(page)).status).toBe(200);
});

test("upgrading an HTTP session to HTTPS protects both cookies and preserves the session", async ({ page, context }) => {
  const httpOrigin = "http://vocat.test:17477";
  await page.goto(httpOrigin + "/login?redirect=%2Fsettings");
  await page.locator('input[autocomplete="username"]').fill("admin");
  await page.locator('input[autocomplete="current-password"]').fill(PASSWORD);
  await page.getByRole("button", { name: "Sign In", exact: true }).click();
  await expect(page).toHaveURL(httpOrigin + "/settings");
  expect((await checkSession(page)).status).toBe(200);
  const original = (await context.cookies(httpOrigin)).find(c => c.name === "vocat_session");
  expect(original?.secure).toBe(false);

  // The fixture terminates TLS without enabling forwarded-header trust.
  await page.goto(APP + "/settings");
  expect((await checkSession(page)).status).toBe(200);
  for (const name of ["vocat_session", "vocat_csrf"]) {
    const cookie = (await context.cookies(APP)).find(c => c.name === name);
    expect(cookie?.secure).toBe(true);
    expect(cookie?.sameSite).toBe("Strict");
    expect(cookie?.httpOnly).toBe(name === "vocat_session");
  }
  const upgraded = (await context.cookies(APP)).find(c => c.name === "vocat_session");
  expect(upgraded?.value).toBe(original?.value);
  // Max-Age is rounded to whole seconds; refreshing must not extend the session.
  expect(Math.abs(upgraded!.expires - original!.expires)).toBeLessThanOrEqual(1);
  const writeStatus = await page.evaluate(async () => {
    const session = await (await fetch("/api/auth/session", { referrerPolicy: "no-referrer" })).json();
    return (await fetch("/api/settings/security", {
      method: "PUT", headers: { "Content-Type": "application/json", "X-CSRF-Token": session.data.csrf_token },
      body: JSON.stringify({ mode: "internal" }),
    })).status;
  });
  expect(writeStatus).toBe(200);

  await page.goto(httpOrigin + "/login");
  expect((await checkSession(page)).status).toBe(401);
  expect(await page.evaluate(async () => (await fetch("/api/settings/security")).status)).toBe(401);
  await page.goto(APP + "/settings");
  expect((await checkSession(page)).status).toBe(200);
});
