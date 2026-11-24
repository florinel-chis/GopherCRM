import { test, expect, request as playwrightRequest, Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Documentation captures for the Settings section.
 *
 * All the screens require an authenticated session and /settings/configuration
 * is additionally admin-only, so every test logs in as the seeded admin first.
 *
 * Profile shows the account details and the Change password form; API Keys
 * lists the caller's keys with create (optional expiry) and revoke; the
 * Configuration screen edits a value or resets it to its default. Nothing is
 * saved here: the create, edit and reset dialogs are captured open and then
 * cancelled, never confirmed. The listed API key is created through the API
 * with a fixed name, only when missing, so the page never shows a generated
 * key (the API answers it once, and this spec discards it).
 */

const AREA = 'settings';
const DEMO_KEY_NAME = 'Zapier sync';
const EDITED_KEY = 'general.company_name';
const LOADED = { timeout: 30 * 1000 };

test.describe.configure({ mode: 'serial' });

/** Only the permanent drawer is visible at desktop widths; the temporary one is display:none. */
function visibleNavButton(page: Page, label: string) {
  return page.locator('nav [role="button"]:visible', { hasText: label });
}

/** Opens the collapsed "Settings" group in the sidebar so captures show the section context. */
async function expandSettingsNav(page: Page): Promise<void> {
  const apiKeysItem = visibleNavButton(page, 'API Keys');
  if (await apiKeysItem.count() === 0) {
    await visibleNavButton(page, 'Settings').click();
  }
  await expect(apiKeysItem).toBeVisible();
}

/** Creates the demo API key through the API when no active key of that name exists. */
async function ensureDemoApiKey(): Promise<void> {
  const api = await playwrightRequest.newContext();
  try {
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };

    const listed = await api.get(`${API_BASE_URL}/api-keys`, { headers, params: { limit: 100 } });
    expect(listed.status()).toBe(200);
    const keys: Array<{ name: string; is_active: boolean }> = (await listed.json()).data ?? [];
    if (keys.some((key) => key.name === DEMO_KEY_NAME && key.is_active)) {
      return;
    }
    // The response carries the generated key once; it is not read or kept.
    const created = await api.post(`${API_BASE_URL}/api-keys`, { headers, data: { name: DEMO_KEY_NAME } });
    expect(created.status(), 'POST /api-keys').toBe(201);
  } finally {
    await api.dispose();
  }
}

/** Opens the configuration screen and waits for the General tab's values. */
async function openConfiguration(page: Page): Promise<void> {
  await page.goto('/settings/configuration');

  await expect(page.getByRole('heading', { name: 'Configuration Settings' })).toBeVisible(LOADED);
  // Tab labels carry their per-category counts once the values have loaded.
  await expect(page.getByRole('tab', { name: /^General \(\d+\)$/ })).toBeVisible(LOADED);

  // Wait for the loaded values themselves, not just the shell.
  const rows = page.locator('[role="tabpanel"]:not([hidden]) table tbody tr');
  await expect(rows.first()).toBeVisible(LOADED);
}

test.describe('Settings screenshots', () => {
  test.beforeAll(async () => {
    await ensureDemoApiKey();
  });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(90 * 1000);
    await ensureAdminLoggedIn(page);
  });

  test('profile page', async ({ page }) => {
    await page.goto('/settings/profile');
    await expect(page.getByRole('heading', { name: 'Profile', level: 4 })).toBeVisible(LOADED);
    await expect(page.getByText(testAdminCredentials.email)).toBeVisible(LOADED);
    await expect(page.getByLabel(/^Current password/)).toBeVisible();
    await expect(page.getByRole('button', { name: /change password/i })).toBeVisible();
    await expandSettingsNav(page);

    await capture(page, AREA, '01-profile');
  });

  test('api keys page', async ({ page }) => {
    await page.goto('/settings/api-keys');
    await expect(page.getByRole('heading', { name: 'API Keys', level: 4 })).toBeVisible(LOADED);
    const row = page.getByRole('row').filter({ hasText: DEMO_KEY_NAME });
    await expect(row.first()).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '02-api-keys');
  });

  test('api key create dialog', async ({ page }) => {
    await page.goto('/settings/api-keys');
    await expect(page.getByRole('heading', { name: 'API Keys', level: 4 })).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await page.getByRole('button', { name: /create api key/i }).click();
    const dialog = page.getByRole('dialog', { name: 'Create API Key' });
    await expect(dialog).toBeVisible();
    await dialog.getByLabel(/^Name/).fill('Reporting export');
    const expiry = new Date(Date.now() + 90 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
    await dialog.getByLabel(/^Expires on/).fill(expiry);
    await expect(dialog.getByLabel(/^Expires on/)).toHaveValue(expiry);

    // Captured filled in and never submitted: no key is created.
    await capture(page, AREA, '03-api-key-create');

    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });

  test('configuration settings page', async ({ page }) => {
    await openConfiguration(page);
    await expandSettingsNav(page);

    await capture(page, AREA, '04-configuration', { fullPage: true });
  });

  test('configuration edit dialog', async ({ page }) => {
    await openConfiguration(page);
    await expandSettingsNav(page);

    await page.getByRole('button', { name: `Edit ${EDITED_KEY}` }).click();
    const dialog = page.getByRole('dialog').filter({ hasText: 'Edit Configuration' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText(EDITED_KEY)).toBeVisible();

    await capture(page, AREA, '05-configuration-edit');

    // Cancelled: nothing is saved.
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });

  test('configuration reset confirmation', async ({ page }) => {
    await openConfiguration(page);
    await expandSettingsNav(page);

    await page.getByRole('button', { name: `Reset ${EDITED_KEY}` }).click();
    const confirm = page.getByRole('dialog', { name: 'Reset to default?' });
    await expect(confirm).toBeVisible();
    await expect(confirm).toContainText(EDITED_KEY);

    await capture(page, AREA, '06-configuration-reset');

    // Cancelled, never confirmed: the stored value stays.
    await confirm.getByRole('button', { name: 'Cancel' }).click();
    await expect(confirm).toBeHidden();
  });
});
