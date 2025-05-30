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
    
    // Ensure admin is logged in
    await adminAuth.ensureAdminLoggedIn();
  });

  test.afterEach(async ({ page }) => {
    // Cleanup - logout after each test
    await adminAuth.logout();
  });

  test('admin can view users list page', async ({ page }) => {
    await usersPage.goto();
    
    // Verify page loads correctly
    await expect(usersPage.pageTitle).toBeVisible();
    await expect(usersPage.newUserButton).toBeVisible();
    
    // Verify we can see the table (even if empty)
    await expect(usersPage.usersTable).toBeVisible();
  });

  test('admin can create a new user successfully', async ({ page }) => {
    const userData = generateUserData();
    
    await usersPage.goto();
    await usersPage.clickNewUser();
    
    // Fill the user form
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    
    // Save and wait for response
    const response = await usersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify redirect to users list or detail
    expect(page.url()).toContain('/users');
    
    // Verify success message
    const successMessage = await usersPage.getSuccessMessage();
    expect(successMessage).toBeTruthy();
  });

  test('admin can edit an existing user', async ({ page }) => {
    // First create a user
    const originalUserData = generateUserData();
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...originalUserData,
      confirmPassword: originalUserData.password
    });
    await usersPage.saveUser();
    
    // Go back to users list
    await usersPage.goto();
    
    // Edit the first user (excluding the admin)
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0; // Skip admin if there are multiple users
    
    await usersPage.editUser(userIndex);
    
    // Modify the user data (don't change password on edit)
    const updatedUserData = {
      firstName: 'UpdatedFirst',
      lastName: 'UpdatedLast',
      email: `updated_${Date.now()}@example.com`,
      role: 'support'
    };
    
    await usersPage.fillUserForm(updatedUserData);
    await usersPage.saveUser();
    
    // Verify the update
    await usersPage.goto();
    const userData = await usersPage.getUserData(userIndex);
    expect(userData.firstName).toBe('UpdatedFirst');
    expect(userData.lastName).toBe('UpdatedLast');
    expect(userData.role.toLowerCase()).toContain('support');
  });

  test('admin can view user details', async ({ page }) => {
    // Create a user first
    const userData = generateUserData();
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    await usersPage.saveUser();
    
    // Go back to users list and view the user
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0; // Skip admin if there are multiple users
    
    await usersPage.viewUser(userIndex);
    
    // Verify we're on the user detail page
    expect(page.url()).toMatch(/\/users\/\d+$/);
    
    // Verify user information is displayed
    await expect(page.locator(`text=${userData.firstName}`)).toBeVisible();
    await expect(page.locator(`text=${userData.lastName}`)).toBeVisible();
    await expect(page.locator(`text=${userData.email}`)).toBeVisible();
  });

  test('admin can deactivate and activate users', async ({ page }) => {
    // Create an active user first
    const userData = { ...generateUserData(), isActive: true };
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: userData.password
    });
    await usersPage.saveUser();
    
    // Go back to users list
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0; // Skip admin if there are multiple users
    
    // Deactivate the user
    await usersPage.deactivateUser(userIndex);
    
    // Verify user status changed
    await page.waitForTimeout(1000); // Wait for update
    const deactivatedUserData = await usersPage.getUserData(userIndex);
    expect(deactivatedUserData.status.toLowerCase()).toContain('inactive');
    
    // Activate the user again
    await usersPage.activateUser(userIndex);
    
    // Verify user status changed back
    await page.waitForTimeout(1000); // Wait for update
    const reactivatedUserData = await usersPage.getUserData(userIndex);
    expect(reactivatedUserData.status.toLowerCase()).toContain('active');
  });

  test('admin can search users', async ({ page }) => {
    // Create multiple users with distinct data
    const user1Data = { ...generateUserData(), firstName: 'SearchUser1', lastName: 'TestOne' };
    const user2Data = { ...generateUserData(), firstName: 'SearchUser2', lastName: 'TestTwo' };
    
    // Create first user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...user1Data,
      confirmPassword: user1Data.password
    });
    await usersPage.saveUser();
    
    // Create second user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...user2Data,
      confirmPassword: user2Data.password
    });
    await usersPage.saveUser();
    
    // Test search functionality
    await usersPage.goto();
    await usersPage.searchUsers('SearchUser1');
    
    // Should find the first user
    const searchResults = await usersPage.getUserCount();
    expect(searchResults).toBeGreaterThanOrEqual(1);
    
    // Check if the search result contains the expected user
    let foundUser = false;
    for (let i = 0; i < searchResults; i++) {
      const userData = await usersPage.getUserData(i);
      if (userData.firstName.includes('SearchUser1')) {
        foundUser = true;
        break;
      }
    }
    expect(foundUser).toBe(true);
  });

  test('admin can filter users by role', async ({ page }) => {
    // Create users with different roles
    const salesUserData = { ...generateUserData(), role: 'sales' };
    const supportUserData = { ...generateUserData(), role: 'support' };
    
    // Create sales user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...salesUserData,
      confirmPassword: salesUserData.password
    });
    await usersPage.saveUser();
    
    // Create support user
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...supportUserData,
      confirmPassword: supportUserData.password
    });
    await usersPage.saveUser();
    
    // Filter by 'sales' role
    await usersPage.goto();
    await usersPage.filterByRole('sales');
    
    // Verify filtered results
    const filteredCount = await usersPage.getUserCount();
    expect(filteredCount).toBeGreaterThanOrEqual(1);
    
    // Check that all visible users have 'sales' role
    for (let i = 0; i < Math.min(filteredCount, 3); i++) {
      const userData = await usersPage.getUserData(i);
      expect(userData.role.toLowerCase()).toContain('sales');
    }
  });

  test('admin sees validation errors for invalid user data', async ({ page }) => {
    await usersPage.goto();
    await usersPage.clickNewUser();
    
    // Try to save without required fields
    await usersPage.saveUser();
    
    // Should show validation errors or prevent submission
    const currentUrl = page.url();
    expect(currentUrl).toContain('/users/new'); // Should stay on form page
  });

  test('admin sees validation errors for password mismatch', async ({ page }) => {
    const userData = generateUserData();
    
    await usersPage.goto();
    await usersPage.clickNewUser();
    
    // Fill form with mismatched passwords
    await usersPage.fillUserForm({
      ...userData,
      confirmPassword: 'DifferentPassword123!'
    });
    
    await usersPage.saveUser();
    
    // Should show validation error or prevent submission
    const currentUrl = page.url();
    expect(currentUrl).toContain('/users/new'); // Should stay on form page
    
    // Check for password mismatch error
    const errorMessage = await usersPage.getErrorMessage();
    if (errorMessage) {
      expect(errorMessage.toLowerCase()).toContain('password');
    }
  });

  test('admin can handle user form cancellation', async ({ page }) => {
    await usersPage.goto();
    await usersPage.clickNewUser();
    
    // Fill some data
    const userData = generateUserData();
    await usersPage.firstNameInput.fill(userData.firstName);
    await usersPage.lastNameInput.fill(userData.lastName);
    
    // Cancel the form
    await usersPage.cancelButton.click();
    
    // Should return to users list
    expect(page.url()).toContain('/users');
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create users with different roles', async ({ page }) => {
    // Create users with different roles
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
    
    // Verify all roles are represented in the list
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    expect(userCount).toBeGreaterThanOrEqual(roles.length);
    
    // Check that each role is displayed correctly
    let rolesFound = new Set();
    for (let i = 0; i < Math.min(userCount, 10); i++) {
      const userData = await usersPage.getUserData(i);
      if (roles.includes(userData.role.toLowerCase())) {
        rolesFound.add(userData.role.toLowerCase());
      }
    }
    
    // Should find at least some of the created roles
    expect(rolesFound.size).toBeGreaterThan(0);
  });

  test('admin cannot delete themselves', async ({ page }) => {
    await usersPage.goto();
    
    // Find the admin user (current logged-in user)
    const userCount = await usersPage.getUserCount();
    let adminIndex = -1;
    
    for (let i = 0; i < userCount; i++) {
      const userData = await usersPage.getUserData(i);
      if (userData.role.toLowerCase().includes('admin')) {
        adminIndex = i;
        break;
      }
    }
    
    if (adminIndex >= 0) {
      // Try to delete admin user - should either not have delete button or show error
      const adminRow = usersPage.tableRows.nth(adminIndex);
      const deleteButton = adminRow.locator('button:has-text("Delete")');
      
      // Check if delete button exists for admin
      const hasDeleteButton = await deleteButton.count() > 0;
      
      if (hasDeleteButton) {
        await deleteButton.click();
        
        // Should show error message preventing self-deletion
        const errorMessage = await usersPage.getErrorMessage();
        expect(errorMessage).toBeTruthy();
        expect(errorMessage?.toLowerCase()).toContain('cannot delete');
      } else {
        // No delete button for admin user - this is correct behavior
        expect(hasDeleteButton).toBe(false);
      }
    }
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
    await usersPage.saveUser();
    
    // Try to create second user with same email
    await usersPage.goto();
    await usersPage.clickNewUser();
    
    const duplicateUserData = {
      ...generateUserData(),
      email: userData.email // Same email
    };
    
    await usersPage.fillUserForm({
      ...duplicateUserData,
      confirmPassword: duplicateUserData.password
    });
    await usersPage.saveUser();
    
    // Should show error message or prevent creation
    const errorMessage = await usersPage.getErrorMessage();
    if (errorMessage) {
      expect(errorMessage.toLowerCase()).toContain('email');
    } else {
      // If no error message, should stay on form page
      expect(page.url()).toContain('/users/new');
    }
  });

  test('admin can manage user permissions through roles', async ({ page }) => {
    // Create user with customer role
    const customerUserData = { ...generateUserData(), role: 'customer' };
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.fillUserForm({
      ...customerUserData,
      confirmPassword: customerUserData.password
    });
    await usersPage.saveUser();
    
    // Edit user to change role to sales
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    const userIndex = userCount > 1 ? 1 : 0; // Skip admin if there are multiple users
    
    await usersPage.editUser(userIndex);
    await usersPage.roleSelect.selectOption('sales');
    await usersPage.saveUser();
    
    // Verify role change
    await usersPage.goto();
    const updatedUserData = await usersPage.getUserData(userIndex);
    expect(updatedUserData.role.toLowerCase()).toContain('sales');
    
    // Change role again to support
    await usersPage.editUser(userIndex);
    await usersPage.roleSelect.selectOption('support');
    await usersPage.saveUser();
    
    // Verify final role change
    await usersPage.goto();
    const finalUserData = await usersPage.getUserData(userIndex);
    expect(finalUserData.role.toLowerCase()).toContain('support');
  });
});