import { test } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';

/**
 * Documentation captures for the AEO section: dashboard metrics, the tracked
 * prompt list with its answer drawer, the citation comparisons and the
 * settings page.
 *
 * Unlike the other areas this suite creates nothing: an AEO run spends real
 * provider credit and takes minutes, so the captures photograph whatever the
 * backend already holds. Seed before running — a brand profile, a handful of
 * prompts and at least one completed run (scripts/aeo_live_smoke.sh walks the
 * whole path) — or the charts will be photographed empty.
 */
test.describe.configure({ mode: 'serial' });

test.describe('AEO documentation screenshots', () => {
  test.beforeEach(async ({ page }) => {
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
