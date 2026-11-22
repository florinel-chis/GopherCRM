import { test, expect, request as playwrightRequest } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';
import { apiResponse, LOADED } from '../helpers/api';

/**
 * Settings > SCIM tokens (#490), end to end: the admin opens the screen from
 * the navigation, creates a token and sees it once, then revokes it through
 * the confirmation. The token is checked against the real SCIM endpoint before
 * and after the revoke: 200 while it is live, 401 once the screen revoked it.
 *
 * /scim/v2 lies outside the API prefix, on the backend's origin (not Vite's).
 * The e2e build disables the strict SCIM rate tier, so the two requests are
 * safe. Safe to retry: every run creates a token with a unique name and
 * revokes it; revoked tokens stay listed by design.
 */
test.describe.configure({ mode: 'serial' });

const TOKENS = /\/settings\/scim\/tokens$/;
const TOKEN = /\/settings\/scim\/tokens\/\d+$/;
const SERVICE_PROVIDER_CONFIG = `${new URL(API_BASE_URL).origin}/scim/v2/ServiceProviderConfig`;
const DATE_FALLBACK = '—';

const scimStatus = async (token: string): Promise<number> => {
  const context = await playwrightRequest.newContext();
  try {
    const response = await context.get(SERVICE_PROVIDER_CONFIG, {
      headers: { Authorization: `Bearer ${token}` },
    });
    return response.status();
  } finally {
    await context.dispose();
  }
};

test.describe('Admin - Settings > SCIM tokens', () => {
  test.beforeEach(() => {
    test.setTimeout(120 * 1000);
  });

  test('creates a token shown once, revokes it, and the revoked token is refused on /scim/v2', async ({
    page,
  }) => {
    await new AdminAuthHelper(page).createAndLoginAdmin();

    const navigation = page.getByTestId('app-navigation');
    await navigation.getByText('Settings', { exact: true }).click();
    const firstLoad = apiResponse(page, TOKENS, 'GET');
    await navigation.getByText('SCIM tokens', { exact: true }).click();
    await page.waitForURL(`${UI_BASE_URL}/settings/scim-tokens`);
    expect((await firstLoad).status()).toBe(200);
    await expect(page.getByRole('heading', { name: 'SCIM tokens' })).toBeVisible(LOADED);

    const name = `e2e-scim-${Date.now()}`;
    await page.getByRole('button', { name: 'Create token' }).click();
    const createDialog = page.getByRole('dialog', { name: 'Create SCIM token' });
    await createDialog.getByLabel('Name').fill(name);
    const created = apiResponse(page, TOKENS, 'POST');
    await createDialog.getByRole('button', { name: 'Create', exact: true }).click();
    const createResponse = await created;
    expect(createResponse.status()).toBe(201);
    const body = (await createResponse.json()) as { data: { token: string } };

    const reveal = page.getByRole('dialog', { name: 'Your new SCIM token' });
    const field = reveal.getByTestId('scim-token-value');
    await expect(field).toBeVisible(LOADED);
    const token = await field.inputValue();
    expect(token).toMatch(/^scim_[0-9a-f]{64}$/);
    expect(body.data.token).toBe(token);
    await expect(reveal.getByText(/shown only once and cannot be retrieved again/)).toBeVisible();
    await expect(reveal.getByRole('button', { name: 'Copy SCIM token' })).toBeVisible();

    const reload = apiResponse(page, TOKENS, 'GET');
    await reveal.getByRole('button', { name: 'Done' }).click();
    expect((await reload).status()).toBe(200);
    await expect(reveal).toBeHidden(LOADED);
    expect(await page.content()).not.toContain(token);

    const row = page.getByTestId('scim-token-row').filter({ hasText: name });
    await expect(row).toBeVisible(LOADED);
    // Name, Created, Last used, Revoked, Actions: never used, not revoked.
    const cells = row.getByRole('cell');
    await expect(cells.nth(2)).toHaveText(DATE_FALLBACK);
    await expect(cells.nth(3)).toHaveText(DATE_FALLBACK);
    const revokeButton = row.getByRole('button', { name: `Revoke ${name}` });
    await expect(revokeButton).toBeEnabled();

    // The token works before the revoke, so the 401 afterwards is the
    // revoke's doing and not a token that never worked or a wrong address.
    expect(await scimStatus(token)).toBe(200);

    await revokeButton.click();
    const confirm = page.getByRole('dialog', { name: 'Revoke SCIM token' });
    await expect(confirm.getByText(/will be refused at once/)).toBeVisible(LOADED);
    const revoked = apiResponse(page, TOKEN, 'DELETE');
    await confirm.getByRole('button', { name: 'Revoke', exact: true }).click();
    expect((await revoked).status()).toBe(200);

    // The row stays listed, with a revoked date and the action disabled.
    await expect(row).toBeVisible(LOADED);
    await expect(cells.nth(3)).not.toHaveText(DATE_FALLBACK, LOADED);
    await expect(revokeButton).toBeDisabled(LOADED);

    // The revoke reached the store: the identity provider is now refused.
    expect(await scimStatus(token)).toBe(401);
  });
});
