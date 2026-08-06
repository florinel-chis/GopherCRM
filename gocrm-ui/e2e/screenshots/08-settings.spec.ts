import { test, expect, Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';

/**
 * Documentation captures for the Settings section.
 *
 * All three screens require an authenticated session and /settings/configuration
 * is additionally admin-only, so every test logs in as the seeded admin first.
 *
 * Profile and API Keys are placeholder screens in the current build — they render
 * a heading and nothing else (see src/pages/settings/Profile.tsx and APIKeys.tsx).
 * The captures record that real state; there is no key-creation control to drive,
 * so no "key created" dialog can be produced.
 */

const AREA = 'settings';

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

test.describe('Settings screenshots', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('profile page', async ({ page }) => {
    await page.goto('/settings/profile');
    await expect(page.getByRole('heading', { name: 'Profile' })).toBeVisible();
    await expandSettingsNav(page);

    await capture(page, AREA, '01-profile');
  });

  test('api keys page', async ({ page }) => {
    await page.goto('/settings/api-keys');
    await expect(page.getByRole('heading', { name: 'API Keys' })).toBeVisible();
    await expandSettingsNav(page);

    // The screen exposes no create control yet, so there is nothing to drive here:
    // the capture documents the page exactly as a user finds it.
    await capture(page, AREA, '02-api-keys');
  });

  test('configuration settings page', async ({ page }) => {
    await page.goto('/settings/configuration');

    await expect(page.getByRole('heading', { name: 'Configuration Settings' })).toBeVisible();
    // Tab labels carry their per-category counts once the values have loaded.
    await expect(page.getByRole('tab', { name: /^General \(\d+\)$/ })).toBeVisible();

    // Wait for the loaded values themselves, not just the shell.
    const rows = page.locator('[role="tabpanel"]:not([hidden]) table tbody tr');
    await expect(rows.first()).toBeVisible();

    await expandSettingsNav(page);

    await capture(page, AREA, '04-configuration', { fullPage: true });
  });
});
