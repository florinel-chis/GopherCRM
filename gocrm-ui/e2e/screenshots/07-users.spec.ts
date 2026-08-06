import { test, expect, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { generateUserData } from '../fixtures/admin-user';
import { UsersPage } from '../pages/users.page';

/**
 * Documentation captures for the Users area.
 *
 * Every user shown here is created through the UI by this file. The delete
 * dialog is only ever opened on one of those users and is always cancelled —
 * deleting a user is an irreversible erasure of personal data.
 */

test.describe.configure({ mode: 'serial' });

interface CreatedUser {
  id: number;
  firstName: string;
  lastName: string;
  email: string;
  password: string;
  role: string;
}

const createdUsers: CreatedUser[] = [];

async function createUserThroughUi(page: Page): Promise<CreatedUser> {
  const usersPage = new UsersPage(page);
  const data = generateUserData();

  await page.goto('/users/new');
  await page.waitForLoadState('networkidle');
  await usersPage.firstNameInput.waitFor({ state: 'visible' });

  await usersPage.fillUserForm({ ...data, confirmPassword: data.password });

  const response = await usersPage.saveAndWaitForResponse();
  expect(response.status()).toBe(201);

  const body = await response.json();
  const id = body?.data?.id as number;
  expect(typeof id).toBe('number');

  await page.waitForURL(/\/users$/, { timeout: 15000 });
  await page.waitForLoadState('networkidle');

  return { ...data, id };
}

/** Orders the list newest-first so the users created here sit on the first page. */
async function sortByNewest(page: Page): Promise<void> {
  const createdHeader = page.getByRole('columnheader', { name: 'Created' });

  // Each sort fetch briefly unmounts the table (Loading replaces it), which
  // resets DataTable's internal sort state — a fixed click count can therefore
  // never be relied on to reach "desc". Click until the backend actually
  // receives sort_order=desc.
  let descSeen = false;
  const onResponse = (r: { url(): string }) => {
    if (r.url().includes('/users?') && r.url().includes('sort_order=desc')) {
      descSeen = true;
    }
  };
  page.on('response', onResponse);
  for (let i = 0; i < 5 && !descSeen; i++) {
    await createdHeader.click();
    await page.waitForTimeout(700);
  }
  page.off('response', onResponse);
  expect(descSeen).toBe(true);
  await page.waitForLoadState('networkidle');
}

test.describe('Screenshots - Users', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('01-list', async ({ page }) => {
    const usersPage = new UsersPage(page);

    createdUsers.push(await createUserThroughUi(page));
    createdUsers.push(await createUserThroughUi(page));

    await usersPage.goto();
    await expect(usersPage.usersTable).toBeVisible();

    // Newest first, so both freshly created accounts are on screen next to the
    // longer-lived accounts already in the system.
    await sortByNewest(page);

    for (const user of createdUsers) {
      await expect(
        page.locator('table tbody tr', { hasText: user.email })
      ).toBeVisible();
    }
    expect(await usersPage.getUserCount()).toBeGreaterThanOrEqual(3);

    await capture(page, 'users', '01-list', { fullPage: true });
  });

  test('02-create', async ({ page }) => {
    const usersPage = new UsersPage(page);
    const draft = generateUserData();

    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.firstNameInput.waitFor({ state: 'visible' });

    await usersPage.fillUserForm({ ...draft, confirmPassword: draft.password });

    await expect(usersPage.emailInput).toHaveValue(draft.email);
    await expect(usersPage.firstNameInput).toHaveValue(draft.firstName);
    await expect(page.locator('#role-label')).toBeVisible();

    // Captured before submitting: this draft account is never created.
    await capture(page, 'users', '02-create', { fullPage: true });
  });

  test('03-detail', async ({ page }) => {
    const user = createdUsers[0];
    expect(user).toBeTruthy();

    await page.goto(`/users/${user.id}`);
    await page.waitForLoadState('networkidle');

    await expect(
      page.locator('h4', { hasText: `${user.firstName} ${user.lastName}` })
    ).toBeVisible();
    await expect(page.getByText(user.email).first()).toBeVisible();

    await capture(page, 'users', '03-detail', { fullPage: true });
  });

  test('04-edit', async ({ page }) => {
    const usersPage = new UsersPage(page);
    const user = createdUsers[0];
    expect(user).toBeTruthy();

    await page.goto(`/users/${user.id}/edit`);
    await page.waitForLoadState('networkidle');

    await expect(page.locator('h4', { hasText: 'Edit User' })).toBeVisible();
    await expect(usersPage.emailInput).toHaveValue(user.email);
    await expect(usersPage.firstNameInput).toHaveValue(user.firstName);
    await expect(usersPage.lastNameInput).toHaveValue(user.lastName);

    await capture(page, 'users', '04-edit', { fullPage: true });
  });

  test('05-delete-confirm', async ({ page }) => {
    const usersPage = new UsersPage(page);
    const user = createdUsers[createdUsers.length - 1];
    expect(user).toBeTruthy();

    await usersPage.goto();
    await usersPage.searchInput.fill(user.email);
    await page.waitForLoadState('networkidle');

    const row = page.locator('table tbody tr', { hasText: user.email });
    await expect(row).toHaveCount(1);
    await row.locator('[data-testid="DeleteIcon"]').first().click();

    const dialog = page.getByRole('dialog', { name: 'Delete User' });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Delete User');
    await expect(dialog).toContainText(user.firstName);

    await capture(page, 'users', '05-delete-confirm');

    // Always cancel: deletion erases personal data irreversibly.
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });
});
