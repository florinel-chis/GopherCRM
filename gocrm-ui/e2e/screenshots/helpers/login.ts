import { Page, expect } from '@playwright/test';
import { testAdminCredentials } from '../../fixtures/admin-user';

/**
 * Logs the seeded admin in through the real login form.
 *
 * "Remember me" is ticked so the JWT is persisted to localStorage, the same
 * choice the shared AdminAuthHelper makes (an unticked login keeps it in
 * sessionStorage). Success is judged by the redirect, not by storage.
 */
export async function ensureAdminLoggedIn(page: Page): Promise<void> {
  await page.goto('/login');
  await page.waitForLoadState('networkidle');

  const emailInput = page.locator('input[name="email"]');
  await emailInput.waitFor({ state: 'visible' });
  await emailInput.fill(testAdminCredentials.email);
  await page.locator('input[name="password"]').fill(testAdminCredentials.password);
  await page.locator('input[name="remember_me"]').check();

  const responsePromise = page.waitForResponse(
    (response) =>
      response.url().includes('/auth/login') && response.request().method() === 'POST'
  );
  await page.locator('button[type="submit"]:has-text("Sign In")').click();

  const response = await responsePromise;
  expect(response.status()).toBe(200);

  await page.waitForURL('/', { timeout: 15000 });
  await page.waitForLoadState('networkidle');
}
