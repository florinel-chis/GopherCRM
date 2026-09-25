import { defineConfig, devices } from '@playwright/test';

/* Same convention as playwright.config.ts: scripts/e2e/run.sh sets E2E_UI_PORT
 * and the run starts its own Vite server there, pointed at its own backend. */
const e2eUiPort = process.env.E2E_UI_PORT;
const uiBaseURL = `http://localhost:${e2eUiPort ?? '5173'}`;

/**
 * Documentation screenshot suite.
 *
 * Walks every user-facing screen and saves retina captures to
 * ../docs/screenshots/<area>/. Not part of the regular E2E run:
 * invoke with `npm run screenshots` (backend must be up, same as E2E), or
 * against a fresh e2e database and free ports with
 * `scripts/e2e/run.sh --config=playwright.config.screenshots.ts [specs]`.
 */
export default defineConfig({
  testDir: './e2e/screenshots',
  /* Seed the admin account the suites log in as */
  globalSetup: './e2e/global-setup.ts',
  /* One shared database — never parallelise */
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  timeout: 60 * 1000,
  use: {
    baseURL: uiBaseURL,
    trace: 'off',
    screenshot: 'off',
    video: 'off',
    actionTimeout: 10 * 1000,
    navigationTimeout: 30 * 1000,
  },
  projects: [
    {
      name: 'chromium',
      use: {
        ...devices['Desktop Chrome'],
        /* Consistent frame and retina density for the published images */
        viewport: { width: 1440, height: 900 },
        deviceScaleFactor: 2,
      },
    },
  ],
  webServer: {
    command: e2eUiPort ? `npm run dev -- --port ${e2eUiPort} --strictPort` : 'npm run dev',
    url: uiBaseURL,
    reuseExistingServer: !e2eUiPort,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 120 * 1000,
  },
});
