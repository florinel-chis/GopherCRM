import { test } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { mockAeoApi } from './helpers/aeo-fixtures';

/**
 * Documentation captures for the AEO section: dashboard metrics, the tracked
 * prompt list with its answer drawer, the citation comparisons and the
 * settings page.
 *
 * Unlike the other areas, the AEO API is answered from fixed fixtures
 * (helpers/aeo-fixtures.ts): GopherCRM as the tracked brand against fictional
 * competitors. A real run spends provider credit, takes minutes and would
 * photograph whatever brand the backend happens to hold. Login, navigation
 * and the configuration keys still use the real backend.
 */
test.describe.configure({ mode: 'serial' });

test.describe('AEO documentation screenshots', () => {
  test.beforeEach(async ({ page }) => {
    await mockAeoApi(page);
    await ensureAdminLoggedIn(page);
  });

  test('dashboard: visibility gauge, per-engine series, share of voice', async ({ page }) => {
    await page.goto('/aeo');
    await page.waitForLoadState('networkidle');
    // recharts animates lines left-to-right for ~1.5s after mount; Playwright's
    // animations:'disabled' does not cover JS-driven SVG, so capturing too
    // early photographs a half-drawn line.
    await page.waitForTimeout(2000);
    await capture(page, 'aeo', 'aeo-dashboard', { fullPage: true });
  });

  test('prompts: tracked list and per-engine answer drawer', async ({ page }) => {
    await page.goto('/aeo/prompts');
    await page.waitForLoadState('networkidle');
    await capture(page, 'aeo', 'aeo-prompts');

    // The prompt text itself opens the transcript drawer.
    await page.locator('tbody tr').first().getByRole('button').first().click();
    await page.getByTestId('answer-transcript').first().waitFor({ state: 'visible' });
    await capture(page, 'aeo', 'aeo-prompt-answers');
  });

  test('citations: owned-domain rate vs competitors', async ({ page }) => {
    await page.goto('/aeo/citations');
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(2000); // let the recharts bar animation finish
    await capture(page, 'aeo', 'aeo-citations', { fullPage: true });
  });

  test('settings: brand profile, engines and schedule', async ({ page }) => {
    await page.goto('/aeo/settings');
    await page.waitForLoadState('networkidle');
    await capture(page, 'aeo', 'aeo-settings', { fullPage: true });
  });
});
