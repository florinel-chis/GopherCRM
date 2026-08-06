import { defineConfig, devices } from '@playwright/test';

/**
 * Documentation screenshot suite.
 *
 * Walks every user-facing screen and saves retina captures to
 * ../docs/screenshots/<area>/. Not part of the regular E2E run:
 * invoke with `npm run screenshots` (backend must be up, same as E2E).
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
    baseURL: 'http://localhost:5173',
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
    command: 'npm run dev',
    url: 'http://localhost:5173',
    reuseExistingServer: true,
    stdout: 'ignore',
    stderr: 'pipe',
    timeout: 120 * 1000,
  },
});
