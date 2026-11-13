import { test, expect } from '@playwright/test';
import { loadEnv } from 'vite';

// The signed-out pages show the build-time product name (#303): VITE_APP_NAME
// inlined by Vite, else 'GoCRM'. The dev server the suite runs against resolves
// VITE_APP_NAME the way loadEnv does in development mode (the process
// environment over gocrm-ui/.env*), so the expected name is derived the same
// way instead of being hard-coded: a developer whose local .env brands the app
// still gets a passing run, and `VITE_APP_NAME='Fork CRM' npx playwright test`
// on a fresh dev server exercises a branded name.
//
// Public registration is opened for the whole suite by e2e/global-setup.ts and
// restored by global-teardown.ts; this spec does not toggle it.
const FALLBACK_PRODUCT_NAME = 'GoCRM';

function expectedProductName(): string {
  const value = loadEnv('development', process.cwd(), 'VITE_').VITE_APP_NAME;
  return typeof value === 'string' && value.trim() !== '' ? value : FALLBACK_PRODUCT_NAME;
}

test.describe('Build-time product name on the signed-out pages (#303)', () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  const productName = expectedProductName();

  test.beforeEach(async ({ page, context }) => {
    await context.clearCookies();
    await page.goto('/login');
    await page.evaluate(() => {
      localStorage.clear();
      sessionStorage.clear();
    });
  });

  test('the sign-in page welcomes the visitor with the build-time name', async ({ page }) => {
    await page.goto('/login');
    await expect(page.getByText(`Welcome to ${productName}`, { exact: true })).toBeVisible();
  });

  test('the registration page names the build-time product', async ({ page }) => {
    await page.goto('/register');
    await expect(
      page.getByText(`Create your ${productName} account`, { exact: true })
    ).toBeVisible();
  });
});
