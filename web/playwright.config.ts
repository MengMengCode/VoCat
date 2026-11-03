import { defineConfig } from "@playwright/test";
import { fileURLToPath } from "node:url";

export default defineConfig({
  testDir: "./e2e",
  testMatch: "**/*.spec.ts",
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: "https://vocat.test:17475",
    browserName: "chromium",
    ignoreHTTPSErrors: true, // Ephemeral fixture CA; production always validates TLS.
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    launchOptions: {
      args: ["--host-resolver-rules=MAP vocat.test 127.0.0.1, MAP pocket.test 127.0.0.1", "--no-proxy-server"],
    },
  },
  webServer: {
    command: `${process.env.VOCAT_GO || "go"} run ./web/e2e/fixture`,
    cwd: fileURLToPath(new URL("..", import.meta.url)),
    url: "https://127.0.0.1:17475/__e2e/ready",
    ignoreHTTPSErrors: true,
    reuseExistingServer: false,
    timeout: 180_000,
  },
});
