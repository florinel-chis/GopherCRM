import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { UsersPage } from '../pages/users.page';
import { generateUserData } from '../fixtures/admin-user';

test.describe('Admin - Users Management', () => {
  let adminAuth: AdminAuthHelper;
  let usersPage: UsersPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    usersPage = new UsersPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view users list page', async ({ page }) => {
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

    // Go back and edit
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0;

    await usersPage.editUser(userIndex);

    // Update fields (no password on edit)
    await usersPage.firstNameInput.clear();
    await usersPage.firstNameInput.fill('UpdatedFirst');
    await usersPage.lastNameInput.clear();
    await usersPage.lastNameInput.fill('UpdatedLast');
    await usersPage.saveButton.click();

    await page.waitForURL(/\/users(?!.*edit)/, { timeout: 10000 });
  });

  test('admin can view user details', async ({ page }) => {
    const userData = generateUserData();
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    await usersPage.saveAndWaitForResponse();

    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0;

    await usersPage.viewUser(userIndex);
    expect(page.url()).toMatch(/\/users\/\d+$/);
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
    await page.waitForTimeout(1000);
  });

  test('admin can filter users by role', async ({ page }) => {
    const salesUserData = { ...generateUserData(), role: 'sales' };

    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...salesUserData,
      confirmPassword: salesUserData.password
    });
    await usersPage.saveAndWaitForResponse();

    await usersPage.goto();
    await usersPage.filterByRole('sales');
    await page.waitForTimeout(1000);

    const filteredCount = await usersPage.getUserCount();
    expect(filteredCount).toBeGreaterThanOrEqual(1);
  });

  test('admin sees validation errors for invalid user data', async ({ page }) => {
    await usersPage.goto();
    await usersPage.clickNewUser();

    // Try to save without required fields
    await usersPage.saveButton.click();

    // Should stay on form page
    expect(page.url()).toContain('/users/new');
  });

  test('admin sees validation errors for password mismatch', async ({ page }) => {
    const userData = generateUserData();

    await usersPage.goto();
    await usersPage.clickNewUser();

    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: 'DifferentPassword123!'
    });

    await usersPage.saveButton.click();

    // Should stay on form page
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

  test('admin can create users with different roles', async ({ page }) => {
    const roles = ['sales', 'support', 'customer'];

    for (const role of roles) {
      const userData = {
        ...generateUserData(),
        firstName: `${role.charAt(0).toUpperCase() + role.slice(1)}User`,
        role
      };

      await usersPage.goto();
      await usersPage.clickNewUser();
      await usersPage.fillUserForm({
        ...userData,
        confirmPassword: userData.password
      });
      await usersPage.saveUser();
    }

    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    expect(userCount).toBeGreaterThanOrEqual(roles.length);
  });

  test('admin can handle duplicate email validation', async ({ page }) => {
    const userData = generateUserData();

    // Create first user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    await usersPage.saveAndWaitForResponse();

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
    await usersPage.saveButton.click();

    // Should show error or stay on form
    await page.waitForTimeout(2000);
    const url = page.url();
    const error = await usersPage.getErrorMessage();
    expect(url.includes('/new') || (error && error.toLowerCase().includes('email'))).toBeTruthy();
  });

  test('admin can manage user permissions through roles', async ({ page }) => {
    const customerUserData = { ...generateUserData(), role: 'customer' };
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...customerUserData,
      confirmPassword: customerUserData.password
    });
    await usersPage.saveUser();

    // Edit user to change role
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0;

    await usersPage.editUser(userIndex);
    await usersPage.selectMuiOption('role', 'sales');
    await usersPage.saveButton.click();

    await page.waitForURL(/\/users(?!.*edit)/, { timeout: 10000 });
  });
});
