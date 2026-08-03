import { defineConfig, devices } from "@playwright/test";
import path from "node:path";

/**
 * E2E runs against the real read-only web UI. The Go binary is built and served
 * over a fixture data root that global-setup.ts seeds with a known board (one
 * ticket in each control state). The server reads the log per request, so the
 * seed only needs to exist before the tests make requests.
 */
const repoRoot = path.resolve(__dirname, "..");
const bin = path.join(__dirname, ".bin", "draiver");
const fixtureDir = path.join(__dirname, ".fixture-data");
const PORT = 7788;

export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [["list"]],
  timeout: 30_000,
  expect: { timeout: 10_000 },
  globalSetup: "./global-setup.ts",
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: {
    command: `go build -o "${bin}" . && "${bin}" webui --data "${fixtureDir}" --addr 127.0.0.1:${PORT}`,
    cwd: repoRoot,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    stdout: "pipe",
    stderr: "pipe",
  },
});
