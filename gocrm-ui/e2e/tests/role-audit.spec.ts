import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { UI_BASE_URL } from '../helpers/env';
import { APIClient } from '../helpers/sso';
import { LOADED } from '../helpers/api';

/**
 * Settings > Role audit (#552), end to end: the admin creates a sales user
 * through the API, changes its email, deactivates it, activates it and
 * erases it; the screen, opened from the navigation, shows those four changes
 * as the newest rows for that user id with method Session. A sales user is
 * not offered the entry.
 *
 * Safe to retry: every user the spec creates is erased by the spec itself or,
 * when a step failed before that, in afterAll. The rows are picked by the
 * target user id, so rows of other specs and of the account creation (a
 * User role changed row for the same id) do not matter.
 */
test.describe.configure({ mode: 'serial' });

const SALES_PASSWORD = 'Audit!Sales2026x';

test.describe('Admin - Settings > Role audit', () => {
  const createdUserIds: number[] = [];

  test.beforeEach(() => {
    test.setTimeout(120 * 1000);
  });

  test.afterAll(async () => {
    if (createdUserIds.length === 0) return;
    const admin = await APIClient.admin();
    try {
      for (const id of createdUserIds) {
        // 404 for a user the spec already erased.
        await admin.deleteUser(id);
      }
    } finally {
      await admin.dispose();
    }
  });

  test('lists the email change, deactivation, activation and erasure of a user', async ({
    page,
  }) => {
    const tag = Date.now().toString(36);
    const admin = await APIClient.admin();
    let targetId: number;
    try {
      const created = await admin.createUser(
        `e2e-role-audit-${tag}@example.com`,
        SALES_PASSWORD,
        'sales',
      );
      targetId = created.id;
      createdUserIds.push(targetId);

      const moved = await admin.put(`/users/${targetId}`, {
        email: `e2e-role-audit-moved-${tag}@example.com`,
      });
      expect(moved.status(), `PUT /users/${targetId}: ${await moved.text()}`).toBe(200);
      await admin.setActive(targetId, false);
      await admin.setActive(targetId, true);
      const erased = await admin.delete(`/users/${targetId}`);
      expect(erased.status(), `DELETE /users/${targetId}`).toBe(204);
    } finally {
      await admin.dispose();
    }

    await new AdminAuthHelper(page).createAndLoginAdmin();
    const navigation = page.getByTestId('app-navigation');
    await navigation.getByText('Settings', { exact: true }).click();
    const firstLoad = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith('/roles/audit') &&
        response.request().method() === 'GET',
    );
    await navigation.getByText('Role audit', { exact: true }).click();
    await page.waitForURL(`${UI_BASE_URL}/settings/role-audit`);
    expect((await firstLoad).status()).toBe(200);

    // Newest first: the four changes, then the role the account was created
    // with.
    const rows = page.getByTestId('role-audit-row').filter({
      has: page.getByTestId('role-audit-target').getByText(String(targetId), { exact: true }),
    });
    await expect(rows.first()).toBeVisible(LOADED);
    const actions = ['Account erased', 'Account activated', 'Account deactivated', 'Email changed'];
    for (const [index, action] of actions.entries()) {
      const cells = rows.nth(index).getByRole('cell');
      await expect(cells.nth(1)).toHaveText(action);
      await expect(cells.nth(2)).toHaveText('Session');
    }
  });

  test('a sales user is not shown the entry', async ({ browser }) => {
    const admin = await APIClient.admin();
    const email = `e2e-role-audit-sales-${Date.now().toString(36)}@example.com`;
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
      await expect(navigation.getByText('Role audit', { exact: true })).toHaveCount(0);
    } finally {
      await context.close();
    }
  });
});
