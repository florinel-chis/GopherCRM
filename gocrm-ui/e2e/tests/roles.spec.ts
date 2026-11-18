import { test, expect, type Locator, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LoginPage } from '../pages/login.page';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';

/**
 * Custom roles, end to end: an admin clones the Sales system role in
 * Settings > Roles, takes the deal permissions out of the copy and gives the
 * role to a new user in the user form. That user signs in in a browser
 * context of its own and gets Leads but no Deals; when the admin grants
 * "View deals" again, the user's open window picks it up on its next focus.
 *
 * One serial test. It is safe to retry against the same database: the user's
 * email is unique per attempt, and the test starts by removing what an
 * earlier attempt left behind (the users first, since a role that is still
 * held cannot be deleted).
 */
test.describe.configure({ mode: 'serial' });

const ROLE_NAME = 'Field Sales';
const EMAIL_PREFIX = 'e2e-roles-field-';
// Meets the password policy: 10+ characters, upper, lower, digit, special.
const USER_PASSWORD = 'Field!Sales2026x';

const uniqueEmail = () =>
  `${EMAIL_PREFIX}${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}@example.com`;

/** Deletes the users and the role of an earlier attempt, through the API. */
const removeLeftovers = async (page: Page, token: string) => {
  const headers = { Authorization: `Bearer ${token}` };

  const users = await page.request.get(`${API_BASE_URL}/users`, {
    headers,
    params: { search: EMAIL_PREFIX, limit: 100 },
  });
  expect(users.ok()).toBeTruthy();
  const userRows: Array<{ id: number; email: string }> = (await users.json()).data ?? [];
  for (const row of userRows.filter((candidate) => candidate.email.startsWith(EMAIL_PREFIX))) {
    const removed = await page.request.delete(`${API_BASE_URL}/users/${row.id}`, { headers });
    expect(removed.ok(), `delete leftover user ${row.id}`).toBeTruthy();
  }

  const roles = await page.request.get(`${API_BASE_URL}/roles`, { headers, params: { limit: 100 } });
  expect(roles.ok()).toBeTruthy();
  const roleRows: Array<{ id: number; name: string; is_system: boolean }> =
    (await roles.json()).data ?? [];
  for (const row of roleRows.filter((candidate) => !candidate.is_system && candidate.name === ROLE_NAME)) {
    const removed = await page.request.delete(`${API_BASE_URL}/roles/${row.id}`, { headers });
    expect(removed.ok(), `delete leftover role ${row.id}`).toBeTruthy();
  }
};

/** The permission checkbox with this title, a group parent or a single permission. */
const permissionBox = (page: Page, title: string): Locator =>
  page.getByRole('checkbox', { name: title, exact: true });

/** Presses Save in the role editor and waits for the write and the return to the list. */
const saveRole = async (page: Page, method: 'POST' | 'PUT') => {
  const saved = page.waitForResponse(
    (response) => /\/roles(\/\d+)?$/.test(new URL(response.url()).pathname) && response.request().method() === method,
  );
  await page.getByRole('button', { name: 'Save' }).click();
  expect((await saved).ok()).toBeTruthy();
  await page.waitForURL('**/settings/roles');
};

test.describe('Custom roles', () => {
  test('a cloned role decides what its holder sees, and a re-granted permission arrives on focus', async ({
    page,
    browser,
  }) => {
    test.setTimeout(120 * 1000);

    const adminAuth = new AdminAuthHelper(page);
    await adminAuth.ensureAdminLoggedIn();
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeTruthy();
    await removeLeftovers(page, token as string);

    // Clone Sales as "Field Sales".
    await page.goto('/settings/roles');
    await expect(page.getByRole('heading', { name: 'Roles' })).toBeVisible();
    await page.getByRole('button', { name: 'Clone Sales', exact: true }).click();
    const cloneDialog = page.getByRole('dialog', { name: 'Clone role' });
    await cloneDialog.getByLabel(/Name of the new role/).fill(ROLE_NAME);
    await cloneDialog.getByRole('button', { name: 'Clone', exact: true }).click();
    await page.waitForURL(/\/settings\/roles\/\d+$/);
    const roleId = Number(new URL(page.url()).pathname.split('/').pop());
    expect(roleId).toBeGreaterThan(0);

    // The copy holds what Sales holds: some of the five deal permissions,
    // "View deals" among them at scope own.
    const dealsGroup = permissionBox(page, 'Deals');
    const viewDeals = permissionBox(page, 'View deals');
    await expect(page.getByRole('textbox', { name: /^Name/ })).toHaveValue(ROLE_NAME);
    await expect(viewDeals).toBeChecked();
    await expect(page.getByRole('combobox', { name: 'Scope of View deals' })).toHaveValue('own');

    // Untick the deals group: a partly ticked parent first ticks every
    // child, the next click clears them all.
    if (!(await dealsGroup.isChecked())) {
      await dealsGroup.click();
      await expect(dealsGroup).toBeChecked();
    }
    await dealsGroup.click();
    await expect(dealsGroup).not.toBeChecked();
    await expect(dealsGroup).toHaveAttribute('data-indeterminate', 'false');
    for (const title of [
      'View deals',
      'Create deals',
      'Edit deals and change their stage',
      'Delete deals',
      'Give a deal to another owner',
    ]) {
      await expect(permissionBox(page, title)).not.toBeChecked();
    }
    await saveRole(page, 'PUT');

    // Create a user on the role through the user form.
    const email = uniqueEmail();
    await page.goto('/users/new');
    await page.locator('input[name="email"]').fill(email);
    await page.locator('input[name="password"]').fill(USER_PASSWORD);
    await page.locator('input[name="confirmPassword"]').fill(USER_PASSWORD);
    await page.locator('input[name="first_name"]').fill('Fiona');
    await page.locator('input[name="last_name"]').fill('Field');
    await page.getByRole('combobox', { name: /Role/ }).click();
    await page.getByRole('option', { name: ROLE_NAME, exact: true }).click();
    const created = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith('/users') && response.request().method() === 'POST',
    );
    await page.locator('button[type="submit"]').click();
    const createResponse = await created;
    expect(createResponse.status()).toBe(201);
    expect(createResponse.request().postDataJSON()).toMatchObject({ role: 'custom', role_id: roleId });
    await page.waitForURL('**/users');

    // The new user signs in in a browser context of its own.
    const userContext = await browser.newContext();
    try {
      const userPage = await userContext.newPage();
      await userPage.goto(`${UI_BASE_URL}/login`);
      const login = new LoginPage(userPage);
      await login.emailInput.fill(email);
      await login.passwordInput.fill(USER_PASSWORD);
      await login.submit();
      await userPage.waitForURL(`${UI_BASE_URL}/`);

      const navigation = userPage.getByTestId('app-navigation');
      const leadsEntry = navigation.getByText('Leads', { exact: true });
      const dealsEntry = navigation.getByText('Deals', { exact: true });
      await expect(leadsEntry).toBeVisible();
      await expect(dealsEntry).toHaveCount(0);

      await userPage.goto(`${UI_BASE_URL}/deals`);
      await userPage.waitForURL(`${UI_BASE_URL}/unauthorized`);

      // Back in the app, still without Deals, before the admin grants it.
      await userPage.goto(`${UI_BASE_URL}/`);
      await expect(leadsEntry).toBeVisible();
      await expect(dealsEntry).toHaveCount(0);

      // The admin ticks the single "View deals" permission again.
      await page.goto(`/settings/roles/${roleId}`);
      await expect(viewDeals).not.toBeChecked();
      await viewDeals.check();
      await expect(dealsGroup).toHaveAttribute('data-indeterminate', 'true');
      await saveRole(page, 'PUT');

      // The user's window regains focus: the permissions are read again.
      await userPage.evaluate(() => window.dispatchEvent(new Event('focus')));
      await expect(dealsEntry).toBeVisible();
    } finally {
      await userContext.close();
    }
  });
});
