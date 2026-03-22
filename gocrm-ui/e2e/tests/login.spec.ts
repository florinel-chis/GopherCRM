import { test, expect } from '@playwright/test';
import { LoginPage } from '../pages/login.page';

// These credentials must exist in the database before running tests.
// Created via: POST /api/v1/auth/register with role "admin"
const ADMIN_EMAIL = 'admin@gophercrm.local';
const ADMIN_PASSWORD = 'GopherCRM2024!';

test.describe('Login Flow', () => {
  let loginPage: LoginPage;

  test.beforeEach(async ({ page }) => {
    loginPage = new LoginPage(page);
    await loginPage.goto();
  });

  test('login page renders correctly', async ({ page }) => {
    // Verify page elements are present
    await expect(loginPage.emailInput).toBeVisible();
    await expect(loginPage.passwordInput).toBeVisible();
    await expect(loginPage.submitButton).toBeVisible();
    await expect(loginPage.signUpLink).toBeVisible();

    // Verify page title or heading
    await expect(page.locator('text=Sign In').first()).toBeVisible();
  });

  test('successful login redirects to dashboard', async ({ page }) => {
    // Set up response listener
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/auth/login') && response.request().method() === 'POST'
    );

    await loginPage.login(ADMIN_EMAIL, ADMIN_PASSWORD);
    const response = await responsePromise;

    // Verify API response
    expect(response.status()).toBe(200);
    const body = await response.json();
    expect(body.success).toBe(true);
    expect(body.data.token).toBeTruthy();
    expect(body.data.user.email).toBe(ADMIN_EMAIL);

    // Verify redirect to dashboard
    await page.waitForURL('/', { timeout: 10000 });

    // Verify token is stored in localStorage
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeTruthy();
  });

  test('login fails with wrong password', async ({ page }) => {
    await loginPage.login(ADMIN_EMAIL, 'WrongPassword123!');

    // Wait for error message
    const error = await loginPage.getErrorMessage();
    expect(error).toBeTruthy();

    // Should stay on login page
    expect(page.url()).toContain('/login');

    // Token should not be set
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeFalsy();
  });

  test('login fails with non-existent email', async ({ page }) => {
    await loginPage.login('nonexistent@example.com', 'SomePassword123!');

    const error = await loginPage.getErrorMessage();
    expect(error).toBeTruthy();

    expect(page.url()).toContain('/login');
  });

  test('login fails with empty fields', async ({ page }) => {
    // Click submit without entering anything
    await loginPage.submit();

    // Should stay on login page — HTML5 validation prevents submission
    expect(page.url()).toContain('/login');
  });

  test('login fails with invalid email format', async ({ page }) => {
    await loginPage.emailInput.fill('not-an-email');
    await loginPage.passwordInput.fill('SomePassword123!');
    await loginPage.submit();

    // Should stay on login page
    expect(page.url()).toContain('/login');
  });

  test('password visibility toggle works', async ({ page }) => {
    await loginPage.passwordInput.fill('TestPassword123!');

    // Initially hidden
    expect(await loginPage.isPasswordVisible()).toBe(false);

    // Toggle to visible
    await loginPage.togglePasswordVisibility();
    expect(await loginPage.isPasswordVisible()).toBe(true);

    // Toggle back
    await loginPage.togglePasswordVisibility();
    expect(await loginPage.isPasswordVisible()).toBe(false);
  });

  test('can navigate to registration page', async ({ page }) => {
    await loginPage.signUpLink.click();
    await expect(page).toHaveURL('/register');
  });

  test('form can be submitted with Enter key', async ({ page }) => {
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/auth/login') && response.request().method() === 'POST'
    );

    await loginPage.emailInput.fill(ADMIN_EMAIL);
    await loginPage.passwordInput.fill(ADMIN_PASSWORD);
    await loginPage.passwordInput.press('Enter');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    await page.waitForURL('/', { timeout: 10000 });
  });

  test('logged in user can access protected routes', async ({ page }) => {
    // Login first
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/auth/login') && response.request().method() === 'POST'
    );
    await loginPage.login(ADMIN_EMAIL, ADMIN_PASSWORD);
    await responsePromise;
    await page.waitForURL('/', { timeout: 10000 });

    // Navigate to a protected route
    await page.goto('/users');
    await page.waitForLoadState('networkidle');

    // Should not be redirected to login
    expect(page.url()).not.toContain('/login');
  });

  test('unauthenticated user is redirected to login', async ({ page }) => {
    // Clear any existing tokens
    await page.evaluate(() => {
      localStorage.removeItem('gophercrm_token');
      localStorage.removeItem('gophercrm_refresh_token');
    });

    // Try to access a protected route
    await page.goto('/users');
    await page.waitForLoadState('networkidle');

    // Should be redirected to login
    await expect(page).toHaveURL(/\/login/, { timeout: 10000 });
  });
});
