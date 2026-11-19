import { test, expect, request as playwrightRequest, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';
import { APIClient } from '../helpers/sso';

/**
 * Settings > Sign-in audit (#455), end to end: the admin signs in through the
 * login page, one login with an unknown email is refused, and the screen shows
 * both rows, with the outcome filter narrowing to the refusal. A sales user is
 * not offered the entry.
 *
 * The unknown email is under the reserved `.invalid` domain: it belongs to no
 * account, so it is recorded as invalid_credentials without a user id and
 * touches no lockout counter. Safe to retry: the sales user is deleted in
 * afterAll, and the newest rows of the log are this spec's own.
 */
test.describe.configure({ mode: 'serial' });

// The first assertion after a login, a navigation or a data load.
const LOADED = { timeout: 30 * 1000 };
const SALES_PASSWORD = 'Audit!Sales2026x';

const SIGN_INS = /\/audit\/sign-ins$/;

const signInsResponse = (page: Page, outcome?: string) =>
  page.waitForResponse((response) => {
    const url = new URL(response.url());
    return (
      SIGN_INS.test(url.pathname) &&
      response.request().method() === 'GET' &&
      (outcome === undefined || url.searchParams.get('outcome') === outcome)
    );
  });

test.describe('Admin - Settings > Sign-in audit', () => {
  const createdUserIds: number[] = [];

  test.beforeEach(() => {
    test.setTimeout(120 * 1000);
  });

  test.afterAll(async () => {
    if (createdUserIds.length === 0) return;
    const admin = await APIClient.admin();
    try {
      for (const id of createdUserIds) {
        await admin.deleteUser(id);
      }
    } finally {
      await admin.dispose();
    }
  });

  test('lists the admin login and the refused login, and filters by outcome', async ({ page }) => {
    await new AdminAuthHelper(page).createAndLoginAdmin();

    const anonymous = await playwrightRequest.newContext();
    try {
      const refused = await anonymous.post(`${API_BASE_URL}/auth/login`, {
        data: {
          email: `nobody-${Date.now().toString(36)}@sign-in-audit.invalid`,
          password: 'Wrong!Passw0rd-455',
        },
      });
      expect(refused.status()).toBe(401);
    } finally {
      await anonymous.dispose();
    }

    const navigation = page.getByTestId('app-navigation');
    await navigation.getByText('Settings', { exact: true }).click();
    const firstLoad = signInsResponse(page);
    await navigation.getByText('Sign-in audit', { exact: true }).click();
    await page.waitForURL(`${UI_BASE_URL}/settings/sign-in-audit`);
    expect((await firstLoad).status()).toBe(200);

    const rows = page.getByTestId('sign-in-audit-row');
    await expect(
      rows.filter({ hasText: 'password' }).filter({ hasText: 'success' }).first(),
    ).toBeVisible(LOADED);

    const refusedLoad = signInsResponse(page, 'refused');
    await page.getByRole('combobox', { name: 'Outcome' }).click();
    await page.getByRole('option', { name: 'refused', exact: true }).click();
    expect((await refusedLoad).status()).toBe(200);

    await expect(rows.filter({ hasText: 'invalid_credentials' }).first()).toBeVisible(LOADED);
    await expect(rows.filter({ hasText: 'success' })).toHaveCount(0);
  });

  test('a sales user is not shown the entry', async ({ browser }) => {
    const admin = await APIClient.admin();
    const email = `e2e-audit-sales-${Date.now().toString(36)}@example.com`;
    try {
      const created = await admin.createUser(email, SALES_PASSWORD, 'sales');
      createdUserIds.push(created.id);
    } finally {
      await admin.dispose();
    }

    const context = await browser.newContext({ baseURL: UI_BASE_URL });
    try {
      const page = await context.newPage();
      await page.goto('/login');
      await page.locator('input[name="email"]').fill(email);
      await page.locator('input[name="password"]').fill(SALES_PASSWORD);
      await page.getByRole('button', { name: 'Sign In', exact: true }).click();
      await page.waitForURL(`${UI_BASE_URL}/`);
      const navigation = page.getByTestId('app-navigation');
      await expect(navigation.getByText('Leads', { exact: true })).toBeVisible(LOADED);

      await navigation.getByText('Settings', { exact: true }).click();
      await expect(navigation.getByText('Profile', { exact: true })).toBeVisible(LOADED);
      await expect(navigation.getByText('Sign-in audit', { exact: true })).toHaveCount(0);
    } finally {
      await context.close();
    }
  });
});
