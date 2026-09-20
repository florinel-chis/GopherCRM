import { test, expect, type APIRequestContext, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LoginPage } from '../pages/login.page';
import { DashboardPage } from '../pages/dashboard.page';
import { testAdminCredentials } from '../fixtures/admin-user';

// Matches VITE_API_BASE_URL in gocrm-ui/.env — the backend the UI under test talks to.
const API_BASE_URL = 'http://localhost:8090/api/v1';

// Requires a backend started with DISABLE_RATE_LIMIT=true, as e2e/README.md
// documents. This file makes roughly ten calls into the /auth group — register,
// login, change-password, logout, re-login — and the strict tier on that group
// (10/min, burst 5) will otherwise 429 partway through and flake the run. The
// env var lifts the strict tier only; moderate limiting stays on.

interface ThrowawayUser {
  email: string;
  password: string;
}

/**
 * Creates a disposable account through the public registration endpoint.
 *
 * The change-password test rewrites the credentials of whoever it runs as, so
 * it must never run as the shared `test-admin@gocrm.test` account the rest of
 * the suite logs in with. /auth/register always yields a `customer`, which is
 * exactly enough: /settings/profile is behind plain authentication, not a role.
 */
async function registerThrowawayUser(request: APIRequestContext): Promise<ThrowawayUser> {
  const user: ThrowawayUser = {
    email: `settings_${Date.now()}_${Math.random().toString(36).slice(2, 8)}@example.com`,
    password: 'InitialPass1!',
  };

  const response = await request.post(`${API_BASE_URL}/auth/register`, {
    data: {
      email: user.email,
      password: user.password,
      first_name: 'Settings',
      last_name: 'Probe',
    },
  });
  expect(response.status(), `register ${user.email}`).toBe(201);

  return user;
}

/**
 * Logs in through the UI with "Remember me" ticked. Unticked, the app keeps the
 * JWT in sessionStorage only; ticking it puts the token in localStorage, which
 * is what the rest of the helpers probe for.
 */
async function loginThroughUi(page: Page, email: string, password: string): Promise<void> {
  const loginPage = new LoginPage(page);
  await loginPage.goto();
  await loginPage.emailInput.fill(email);
  await loginPage.passwordInput.fill(password);
  await loginPage.rememberMeCheckbox.check();

  const responsePromise = page.waitForResponse(
    (response) => response.url().includes('/auth/login') && response.request().method() === 'POST'
  );
  await loginPage.submit();
  const response = await responsePromise;
  expect(response.status(), `login ${email}`).toBe(200);

  await page.waitForURL('/', { timeout: 15000 });
}

test.describe('Settings — Profile', () => {
  test('shows the signed-in account details', async ({ page }) => {
    const auth = new AdminAuthHelper(page);
    await auth.navigateAsAdmin('/settings/profile');

    await expect(page.getByRole('heading', { name: 'Profile', level: 4 })).toBeVisible();
    await expect(page.getByText(testAdminCredentials.email)).toBeVisible();
    await expect(page.getByText('Account details')).toBeVisible();
    await expect(page.getByRole('button', { name: /change password/i })).toBeVisible();
  });

  test('changes the password and the new one works on the next login', async ({ page, request }) => {
    const user = await registerThrowawayUser(request);
    const newPassword = 'RotatedPass2@';

    await loginThroughUi(page, user.email, user.password);

    await page.goto('/settings/profile');
    await expect(page.getByText(user.email)).toBeVisible();

    await page.getByLabel(/^current password/i).fill(user.password);
    await page.getByLabel(/^new password/i).fill(newPassword);
    await page.getByLabel(/^confirm new password/i).fill(newPassword);

    const changeResponse = page.waitForResponse(
      (response) =>
        response.url().includes('/auth/change-password') && response.request().method() === 'POST'
    );
    await page.getByRole('button', { name: /change password/i }).click();
    expect((await changeResponse).status()).toBe(200);

    // The session survives on purpose: the backend revokes refresh tokens but
    // cannot revoke the JWT already held by this tab.
    await expect(page.getByText(/other sessions have been signed out/i)).toBeVisible();

    const dashboard = new DashboardPage(page);
    await page.goto('/');
    await page.waitForLoadState('networkidle');
    await dashboard.logout();
    await page.waitForURL('/login', { timeout: 15000 });

    await loginThroughUi(page, user.email, newPassword);
    await expect(page).toHaveURL('/');
  });

  test('rejects a wrong current password with the server message', async ({ page, request }) => {
    const user = await registerThrowawayUser(request);
    await loginThroughUi(page, user.email, user.password);

    await page.goto('/settings/profile');
    await page.getByLabel(/^current password/i).fill('DefinitelyWrong1!');
    await page.getByLabel(/^new password/i).fill('AnotherPass3#');
    await page.getByLabel(/^confirm new password/i).fill('AnotherPass3#');
    await page.getByRole('button', { name: /change password/i }).click();

    await expect(page.getByText('The current password is incorrect')).toBeVisible();
  });
});

test.describe('Settings — API Keys', () => {
  test('creates a key, reveals it once, then revokes it', async ({ page }) => {
    const auth = new AdminAuthHelper(page);
    await auth.navigateAsAdmin('/settings/api-keys');

    await expect(page.getByRole('heading', { name: 'API Keys', level: 4 })).toBeVisible();

    const keyName = `E2E key ${Date.now()}`;
    await page.getByRole('button', { name: /create api key/i }).click();

    const createDialog = page.getByRole('dialog', { name: 'Create API Key' });
    await expect(createDialog).toBeVisible();
    await createDialog.getByLabel(/^name/i).fill(keyName);

    const createResponse = page.waitForResponse(
      (response) => response.url().includes('/api-keys') && response.request().method() === 'POST'
    );
    await createDialog.getByRole('button', { name: /^create$/i }).click();
    expect((await createResponse).status()).toBe(201);

    // The plaintext key is shown exactly once, here and nowhere else.
    const revealDialog = page.getByRole('dialog', { name: 'Your new API key' });
    await expect(revealDialog).toBeVisible();
    await expect(revealDialog.getByText(/never be shown again/i)).toBeVisible();

    const revealed = revealDialog.getByTestId('generated-api-key');
    await expect(revealed).toBeVisible();
    expect((await revealed.inputValue()).length).toBeGreaterThan(10);

    await revealDialog.getByRole('button', { name: /done/i }).click();
    await expect(revealDialog).toBeHidden();

    const row = page.getByRole('row').filter({ hasText: keyName });
    await expect(row).toBeVisible();
    await expect(row.getByText('Active')).toBeVisible();

    await row.getByRole('button', { name: `Revoke ${keyName}` }).click();

    const confirmDialog = page.getByRole('dialog', { name: 'Revoke API key' });
    await expect(confirmDialog).toBeVisible();

    const revokeResponse = page.waitForResponse(
      (response) => response.url().includes('/api-keys/') && response.request().method() === 'DELETE'
    );
    await confirmDialog.getByRole('button', { name: /^revoke$/i }).click();
    expect((await revokeResponse).status()).toBe(200);

    // Revoke is a soft operation: the row stays, flipped to inactive.
    await expect(row.getByText('Inactive')).toBeVisible();
    await expect(row.getByRole('button', { name: `Revoke ${keyName}` })).toBeDisabled();
  });
});
