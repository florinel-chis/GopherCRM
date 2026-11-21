import { defineConfig, devices } from '@playwright/test';

/**
 * Read environment variables from file.
 * https://github.com/motdotla/dotenv
 */
// import dotenv from 'dotenv';
// import path from 'path';
// dotenv.config({ path: path.resolve(__dirname, '.env') });

/**
 * `make e2e` (scripts/e2e/run.sh) builds the web app the way the release
 * artifact does (VITE_API_BASE_URL=/api/v1) into E2E_UI_DIST and sets
 * E2E_UI_PORT and E2E_API_ORIGIN: the run then serves that build with
 * `vite preview` on that port, which forwards /api/v1 to E2E_API_ORIGIN
 * (vite.config.ts), and never reuses a server that is already running, which
 * could be serving something else. With E2E_UI_PORT alone the run starts its
 * own dev server on that port. Without either (manual runs) the dev server on
 * 5173 is reused as before.
 */
const e2eUiPort = process.env.E2E_UI_PORT;
const e2eUiDist = process.env.E2E_UI_DIST;
if (e2eUiDist && (!e2eUiPort || !process.env.E2E_API_ORIGIN)) {
  throw new Error(
    'E2E_UI_DIST is set, so E2E_UI_PORT and E2E_API_ORIGIN must be set too: the built web app is served on E2E_UI_PORT and forwards /api/v1 to E2E_API_ORIGIN',
  );
}
const uiBaseURL = `http://localhost:${e2eUiPort ?? '5173'}`;

function webServerCommand(): string {
  if (e2eUiDist) {
    return `npx vite preview --outDir ${JSON.stringify(e2eUiDist)} --port ${e2eUiPort} --strictPort`;
  }
  return e2eUiPort ? `npm run dev -- --port ${e2eUiPort} --strictPort` : 'npm run dev';
}

/**
 * See https://playwright.dev/docs/test-configuration.
 */
export default defineConfig({
  testDir: './e2e/tests',
  /* Seed the admin account the admin suites log in as */
  globalSetup: './e2e/global-setup.ts',
  /* Restore the registration switch to what global setup found */
  globalTeardown: './e2e/global-teardown.ts',
  /* Run tests one by one, not in parallel */
  fullyParallel: false,
  workers: 1,
  /* Fail the build on CI if you accidentally left test.only in the source code. */
  forbidOnly: !!process.env.CI,
  /* Retry on CI only */
  retries: process.env.CI ? 2 : 0,
  /* Reporter to use. See https://playwright.dev/docs/test-reporters */
  reporter: 'html',
  /* Test timeout */
  timeout: 30 * 1000,
  /* Shared settings for all the projects below. See https://playwright.dev/docs/api/class-testoptions. */
  use: {
    /* Base URL to use in actions like `await page.goto('/')`. */
    baseURL: uiBaseURL,

    /* Collect trace when retrying the failed test. See https://playwright.dev/docs/trace-viewer */
    trace: 'on-first-retry',

    /* Take screenshot on failure */
    screenshot: 'only-on-failure',

    /* Record video on failure */
    video: 'retain-on-failure',

    /* Add timeouts for actions */
    actionTimeout: 10 * 1000,
    navigationTimeout: 30 * 1000,
  },

  /* Configure projects for major browsers */
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  /* Serve the built web app (make e2e) or run the dev server before starting the tests */
  webServer: {
    command: webServerCommand(),
    url: uiBaseURL,
    reuseExistingServer: !e2eUiPort && !e2eUiDist,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 120 * 1000, // 2 minutes to start
  },
});