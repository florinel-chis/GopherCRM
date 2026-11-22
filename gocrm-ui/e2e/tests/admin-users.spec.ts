import { test, expect } from '@playwright/test';
import { faker } from '@faker-js/faker';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { UsersPage } from '../pages/users.page';
import { generateUserData } from '../fixtures/admin-user';
import { LoginPage } from '../pages/login.page';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';
import { LOADED } from '../helpers/api';
import { recordCreates, submitEmptyForm } from '../helpers/forms';

test.describe('Admin - Users Management', () => {
  let adminAuth: AdminAuthHelper;
  let usersPage: UsersPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    usersPage = new UsersPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view users list page', async () => {
    await usersPage.goto();

    await expect(usersPage.pageTitle).toBeVisible();
    await expect(usersPage.newUserButton).toBeVisible();
    await expect(usersPage.usersTable).toBeVisible();
  });

  test('admin can create a new user successfully', async ({ page }) => {
    const userData = generateUserData();

    await usersPage.goto();
    await usersPage.clickNewUser();

    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });

    const response = await usersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    expect(page.url()).toContain('/users');
  });

  test('admin can edit an existing user', async ({ page }) => {
    // Create a user first
    const originalUserData = generateUserData();
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...originalUserData,
      confirmPassword: originalUserData.password
    });
    await usersPage.saveAndWaitForResponse();

    // Edit only the user this test created. Picking a row by index could
    // rename a real account, including an admin.
    const ownRow = page.locator('table tbody tr', { hasText: originalUserData.email });
    await usersPage.goto();
    await usersPage.searchUsers(originalUserData.email);
    await expect(ownRow).toHaveCount(1);
    await expect(usersPage.tableRows).toHaveCount(1);
    await usersPage.editUser(originalUserData.email);

    // Update fields (no password on edit)
    await usersPage.firstNameInput.clear();
    await usersPage.firstNameInput.fill('UpdatedFirst');
    await usersPage.lastNameInput.clear();
    await usersPage.lastNameInput.fill('UpdatedLast');
    await usersPage.saveButton.click();

    await page.waitForURL(/\/users(?!.*edit)/, { timeout: 10000 });
    await usersPage.goto();
    await usersPage.searchUsers(originalUserData.email);
    await expect(ownRow).toContainText('UpdatedFirst');
  });

  test('admin can view user details', async ({ page }) => {
    const userData = generateUserData();
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    const created = await usersPage.saveAndWaitForResponse();
    expect(created.status()).toBe(201);
    const userId = (await created.json())?.data?.id;
    expect(typeof userId).toBe('number');

    // Open the user this test created from the list, found by its email.
    await usersPage.goto();
    await usersPage.viewUser(userData.email);
    await expect(page).toHaveURL(new RegExp(`/users/${userId}$`));
  });

  test('admin can search users', async ({ page }) => {
    const user1Data = { ...generateUserData(), firstName: `SearchUser_${Date.now()}` };

    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...user1Data,
      confirmPassword: user1Data.password
    });
    await usersPage.saveAndWaitForResponse();

    await usersPage.goto();
    await usersPage.searchUsers(user1Data.firstName);
    await expect(page.locator('table tbody tr', { hasText: user1Data.firstName })).toHaveCount(1);
    await expect(usersPage.tableRows).toHaveCount(1);
  });

  test('admin can filter users by role (#428)', async ({ page }) => {
    const stamp = `${Date.now()}${faker.string.alphanumeric(6)}`;
    const salesEmail = `sales_${stamp}@example.com`;
    const supportEmail = `support_${stamp}@example.com`;
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    for (const [email, role] of [
      [salesEmail, 'sales'],
      [supportEmail, 'support'],
    ]) {
      const created = await page.request.post(`${API_BASE_URL}/users`, {
        headers: { Authorization: `Bearer ${token}` },
        data: { email, password: 'TempPass123!', first_name: 'Filter', last_name: 'User', role },
      });
      expect(created.status(), `seed ${role} user`).toBe(201);
    }
    const salesRow = usersPage.tableRows.filter({ hasText: salesEmail });
    const supportRow = usersPage.tableRows.filter({ hasText: supportEmail });

    await usersPage.goto();
    await usersPage.searchUsers(stamp);
    await expect(salesRow).toHaveCount(1, { timeout: 30000 });
    await expect(supportRow).toHaveCount(1, { timeout: 30000 });

    const filtered = page.waitForResponse(
      (response) =>
        response.request().method() === 'GET' &&
        new URL(response.url()).pathname.endsWith('/users') &&
        new URL(response.url()).searchParams.get('role') === 'sales',
      { timeout: 30000 },
    );
    await usersPage.filterByRole('sales');
    expect((await filtered).status(), 'role-filtered users list').toBe(200);
    await expect(supportRow).toHaveCount(0, { timeout: 30000 });
    await expect(salesRow).toBeVisible({ timeout: 30000 });
  });

  test('admin sees validation errors for invalid user data', async ({ page }) => {
    await usersPage.goto();
    await usersPage.clickNewUser();
    const creates = recordCreates(page, '/users');

    // Try to save without required fields
    await submitEmptyForm(page, usersPage.emailInput, usersPage.saveButton);

    for (const message of [
      'Invalid email address',
      'Password must be at least 8 characters',
      'First name is required',
      'Last name is required',
    ]) {
      await expect(page.getByText(message, { exact: true })).toBeVisible();
    }
    // The form refused to submit: nothing reached the API.
    expect(creates).toHaveLength(0);
    expect(page.url()).toContain('/users/new');
  });

  test('admin sees validation errors for password mismatch', async ({ page }) => {
    const userData = generateUserData();

    await usersPage.goto();
    await usersPage.clickNewUser();
    const creates = recordCreates(page, '/users');

    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: 'DifferentPassword123!'
    });

    await usersPage.saveButton.click();

    await expect(page.getByText("Passwords don't match", { exact: true })).toBeVisible();
    expect(creates).toHaveLength(0);
    expect(page.url()).toContain('/users/new');
  });

  test('admin can handle user form cancellation', async ({ page }) => {
    await usersPage.goto();
    await usersPage.clickNewUser();

    await usersPage.firstNameInput.fill('Test');
    await usersPage.lastNameInput.fill('User');
    await usersPage.cancelButton.click();

    await page.waitForURL('**/users', { timeout: 10000 });
    expect(page.url()).not.toContain('/new');
  });

  // Each role gets its own test budget, including the admin login. Combining
  // three full browser flows exhausted the default timeout on slower CI runs.
  for (const role of ['sales', 'support', 'customer'] as const) {
    test(`admin can create a ${role} user`, async ({ page }) => {
      const roleLabel = role.charAt(0).toUpperCase() + role.slice(1);
      const userData = {
        ...generateUserData(),
        firstName: `${roleLabel}User`,
        role
      };

      await usersPage.goto();
      await usersPage.clickNewUser();
      await usersPage.fillUserForm({
        ...userData,
        confirmPassword: userData.password
      });
      const response = await usersPage.saveAndWaitForResponse();
      expect(response.status()).toBe(201);
      expect((await response.json()).data).toMatchObject({ email: userData.email, role });
      await expect(page).toHaveURL(/\/users$/);

      await usersPage.searchUsers(userData.email);
      const ownRow = usersPage.tableRows.filter({ hasText: userData.email });
      await expect(ownRow).toHaveCount(1);
      await expect(ownRow.getByRole('cell', { name: userData.email, exact: true })).toBeVisible();
      await expect(ownRow.getByRole('cell', { name: roleLabel, exact: true })).toBeVisible();
    });
  }

  test('admin can handle duplicate email validation', async ({ page }) => {
    const userData = generateUserData();

    // Create first user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    const first = await usersPage.saveAndWaitForResponse();
    expect(first.status()).toBe(201);

    // Try to create second user with same email
    await usersPage.goto();
    await usersPage.clickNewUser();

    const duplicateUserData = {
      ...generateUserData(),
      email: userData.email
    };

    await usersPage.fillUserForm({
      ...duplicateUserData,
      confirmPassword: duplicateUserData.password
    });
    const duplicate = await usersPage.saveAndWaitForResponse();
    expect(duplicate.status()).toBe(409);

    // The form shows the server's message and stays open.
    await expect(
      page.getByRole('alert').filter({ hasText: 'user with this email already exists' })
    ).toBeVisible(LOADED);
    await expect(page).toHaveURL(/\/users\/new$/);
  });

  test('admin can manage user permissions through roles', async ({ page }) => {
    const customerUserData = { ...generateUserData(), role: 'customer' };
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...customerUserData,
      confirmPassword: customerUserData.password
    });
    // Wait for the answer: leaving the form earlier cancels the request.
    const created = await usersPage.saveAndWaitForResponse();
    expect(created.status()).toBe(201);

    // Change the role of the user this test created, never of an existing
    // account (row 1 used to be whoever sorted there, possibly an admin).
    const ownRow = page.locator('table tbody tr', { hasText: customerUserData.email });
    await usersPage.goto();
    await usersPage.searchUsers(customerUserData.email);
    await expect(ownRow).toHaveCount(1);
    await expect(usersPage.tableRows).toHaveCount(1);
    await usersPage.editUser(customerUserData.email);
    await usersPage.selectMuiOption('role', 'sales');
    await usersPage.saveButton.click();

    await page.waitForURL(/\/users(?!.*edit)/, { timeout: 10000 });
    await usersPage.goto();
    await usersPage.searchUsers(customerUserData.email);
    await expect(ownRow).toContainText(/sales/i);
  });

  // The seeded admin is shared by the whole serial suite, so the account whose
  // email changes here is a throwaway admin, signed in in a context of its own
  // and erased at the end. The seeded session is never logged out.
  test('an admin changes its own email with its current password', async ({ page, browser }) => {
    // Two sign-ins and two saves: more than the default budget on a loaded machine.
    test.setTimeout(120 * 1000);
    const seededToken = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(seededToken).toBeTruthy();
    const headers = { Authorization: `Bearer ${seededToken}` };

    const stamp = `${Date.now()}`;
    const password = 'OwnEmail!Pass394';
    const oldEmail = `own_email_${stamp}@example.com`;
    const newEmail = `own_email_${stamp}_changed@example.com`;

    const created = await page.request.post(`${API_BASE_URL}/users`, {
      headers,
      data: { email: oldEmail, password, first_name: 'Olive', last_name: 'Ownmail', role: 'admin' },
    });
    expect(created.status()).toBe(201);
    const ownId: number = (await created.json()).data.id;

    const ownContext = await browser.newContext();
    try {
      const ownPage = await ownContext.newPage();
      await ownPage.goto(`${UI_BASE_URL}/login`);
      const login = new LoginPage(ownPage);
      await login.emailInput.fill(oldEmail);
      await login.passwordInput.fill(password);
      await login.submit();
      await ownPage.waitForURL(`${UI_BASE_URL}/`, { timeout: 30000 });

      const ownForm = new UsersPage(ownPage);
      await ownPage.goto(`${UI_BASE_URL}/users/${ownId}/edit`);
      await expect(ownForm.emailInput).toHaveValue(oldEmail, { timeout: 30000 });
      // An untouched email asks for nothing.
      await expect(ownForm.currentPasswordInput).toHaveCount(0);

      await ownForm.emailInput.fill(newEmail);
      await expect(ownForm.currentPasswordInput).toBeVisible();
      await expect(ownForm.currentPasswordInput).toHaveAttribute('required', '');

      const putOwnAccount = () =>
        ownPage.waitForResponse(
          (response) =>
            new URL(response.url()).pathname.endsWith(`/users/${ownId}`) &&
            response.request().method() === 'PUT',
        );

      // A wrong password is refused and the form stays open with the server's message.
      await ownForm.currentPasswordInput.fill('Wrong!Pass394');
      let answered = putOwnAccount();
      await ownForm.saveButton.click();
      const refused = await answered;
      expect(refused.status()).toBe(400);
      expect((await refused.json()).error.code).toBe('INVALID_CURRENT_PASSWORD');
      await expect(ownPage.getByText('The current password is incorrect')).toBeVisible({ timeout: 30000 });
      await expect(ownPage).toHaveURL(new RegExp(`/users/${ownId}/edit$`));

      // The right one saves.
      await ownForm.currentPasswordInput.fill(password);
      answered = putOwnAccount();
      await ownForm.saveButton.click();
      const saved = await answered;
      expect(saved.status()).toBe(200);
      expect(saved.request().postDataJSON()).toMatchObject({
        email: newEmail,
        current_password: password,
      });
      await ownPage.waitForURL(/\/users$/, { timeout: 30000 });

      // The stored account, read by the seeded admin.
      const stored = await page.request.get(`${API_BASE_URL}/users/${ownId}`, { headers });
      expect(stored.status()).toBe(200);
      expect((await stored.json()).data.email).toBe(newEmail);
    } finally {
      await ownContext.close();
      const removed = await page.request.delete(`${API_BASE_URL}/users/${ownId}`, { headers });
      expect(removed.status()).toBe(204);
    }
  });

  // The counterpart: the same form asks for nothing when the account under
  // edit is not the signed-in one. The account is created here and erased at the end.
  test("an admin changes another user's email without a current password", async ({ page }) => {
    test.setTimeout(90 * 1000);
    const seededToken = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(seededToken).toBeTruthy();
    const headers = { Authorization: `Bearer ${seededToken}` };

    const stamp = `${Date.now()}`;
    const oldEmail = `other_email_${stamp}@example.com`;
    const newEmail = `other_email_${stamp}_changed@example.com`;

    const created = await page.request.post(`${API_BASE_URL}/users`, {
      headers,
      data: {
        email: oldEmail,
        password: 'OtherEmail!Pass394',
        first_name: 'Otto',
        last_name: 'Othermail',
        role: 'sales',
      },
    });
    expect(created.status()).toBe(201);
    const otherId: number = (await created.json()).data.id;

    try {
      await page.goto(`/users/${otherId}/edit`);
      await expect(usersPage.emailInput).toHaveValue(oldEmail, { timeout: 30000 });

      await usersPage.emailInput.fill(newEmail);
      await expect(usersPage.emailInput).toHaveValue(newEmail);
      await expect(usersPage.currentPasswordInput).toHaveCount(0);

      const answered = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith(`/users/${otherId}`) &&
          response.request().method() === 'PUT',
      );
      await usersPage.saveButton.click();
      const saved = await answered;
      expect(saved.status()).toBe(200);
      const body = saved.request().postDataJSON();
      expect(body).toMatchObject({ email: newEmail });
      expect(body).not.toHaveProperty('current_password');
      await page.waitForURL(/\/users$/, { timeout: 30000 });

      const stored = await page.request.get(`${API_BASE_URL}/users/${otherId}`, { headers });
      expect(stored.status()).toBe(200);
      expect((await stored.json()).data.email).toBe(newEmail);
    } finally {
      const removed = await page.request.delete(`${API_BASE_URL}/users/${otherId}`, { headers });
      expect(removed.status()).toBe(204);
    }
  });
});
