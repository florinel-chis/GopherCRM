import { test, expect, type Route } from '@playwright/test';
import { RegisterPage } from '../pages/register.page';
import { DashboardPage } from '../pages/dashboard.page';
import { generateTestUser, testPasswords } from '../fixtures/test-data';
import { setPublicRegistration } from '../helpers/registration-config';
import { API_BASE_URL } from '../helpers/env';
import { apiResponse, LOADED } from '../helpers/api';

test.describe('Registration Flow', () => {
  let registerPage: RegisterPage;
  let dashboardPage: DashboardPage;

  test.beforeEach(async ({ page }) => {
    registerPage = new RegisterPage(page);
    dashboardPage = new DashboardPage(page);
    await registerPage.goto();
  });

  test('successful registration redirects to dashboard', async ({ page }) => {
    const user = generateTestUser();

    await registerPage.fillForm(user);

    const responsePromise = apiResponse(page, /\/auth\/register$/, 'POST');
    await registerPage.submit();
    expect((await responsePromise).status()).toBe(201);

    await expect(page).toHaveURL('/', LOADED);

    // Verify token is stored
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeTruthy();
  });

  test('shows validation errors for empty fields', async () => {
    // Try to submit without filling any fields
    await registerPage.submit();
    
    // HTML5 validation will prevent form submission and show browser's native validation
    // The first required field (first_name) will get focus and show validation message
    const firstNameValidation = await registerPage.checkHTML5ValidationMessage('first_name');
    expect(firstNameValidation).toBeTruthy(); // Browser shows "Please fill out this field" or similar
    
    // Check that the field is marked as invalid
    expect(await registerPage.isFieldInvalid('first_name')).toBe(true);
  });

  test('shows React Hook Form validation for invalid data', async () => {
    // Fill in fields with invalid data to bypass HTML5 required validation
    await registerPage.firstNameInput.fill('Test');
    await registerPage.lastNameInput.fill('User');
    await registerPage.emailInput.fill('invalid-email'); // Invalid format
    await registerPage.passwordInput.fill('short'); // Too short
    await registerPage.confirmPasswordInput.fill('short');
    
    // Submit to trigger React Hook Form validation
    await registerPage.submit();
    
    // Check for custom error messages
    await expect(registerPage.fieldError('email')).toHaveText('Invalid email address');
    await expect(registerPage.fieldError('password')).toContainText('at least 10 characters');
  });

  test('validates email format', async () => {
    // Fill required fields first to bypass HTML5 required validation
    await registerPage.firstNameInput.fill('Test');
    await registerPage.lastNameInput.fill('User');
    await registerPage.passwordInput.fill(testPasswords.allRequirements);
    await registerPage.confirmPasswordInput.fill(testPasswords.allRequirements);

    // Test invalid email format
    await registerPage.emailInput.fill('invalid-email');
    await registerPage.submit();

    await expect(registerPage.fieldError('email')).toHaveText('Invalid email address');
  });

  test('validates password requirements', async () => {
    const user = generateTestUser();
    const passwordError = registerPage.fieldError('password');
    
    // Test too short password (the policy requires 10 characters)
    await registerPage.fillForm({ ...user, password: 'Short1!' });
    await registerPage.submit();
    await expect(passwordError).toContainText('at least 10 characters');

    // Test missing uppercase
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noUppercase });
    await registerPage.submit();
    await expect(passwordError).toContainText('one uppercase letter');
    
    // Test missing lowercase
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noLowercase });
    await registerPage.submit();
    await expect(passwordError).toContainText('one lowercase letter');
    
    // Test missing number
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noNumber });
    await registerPage.submit();
    await expect(passwordError).toContainText('one number');

    // Test missing special character
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: 'TestPassword123' });
    await registerPage.submit();
    await expect(passwordError).toContainText('one special character');
  });

  test('validates password confirmation match', async () => {
    const user = generateTestUser();
    await registerPage.fillForm({
      ...user,
      confirmPassword: 'DifferentPassword123'
    });
    await registerPage.submit();
    
    await expect(registerPage.fieldError('confirmPassword')).toHaveText("Passwords don't match");
  });

  test('shows error for duplicate email registration', async ({ page }) => {
    const user = generateTestUser();
    
    // First registration
    await registerPage.fillForm(user);
    await registerPage.submit();

    // Wait for redirect to dashboard
    await page.waitForURL('/', { timeout: 15000 });
    
    // Logout and try to register again with same email
    await dashboardPage.logout();
    await page.goto('/register');
    
    // Try to register with same email
    await registerPage.fillForm(user);
    await registerPage.submit();
    
    // Should show error
    const error = await registerPage.getGeneralError();
    expect(error).toContain('user with this email already exists');
  });

  test('password visibility toggle works', async () => {
    const password = 'TestPassword123';
    await registerPage.passwordInput.fill(password);
    
    // Initially password should be hidden
    expect(await registerPage.isPasswordVisible()).toBe(false);
    
    // Click visibility toggle
    await registerPage.togglePasswordVisibility();
    
    // Password should be visible
    expect(await registerPage.isPasswordVisible()).toBe(true);
    
    // Toggle back
    await registerPage.togglePasswordVisibility();
    expect(await registerPage.isPasswordVisible()).toBe(false);
  });

  test('form can be submitted with Enter key', async ({ page }) => {
    const user = generateTestUser();
    await registerPage.fillForm(user);
    
    // Press Enter in the last field instead of clicking submit
    await registerPage.confirmPasswordInput.press('Enter');
    
    // Should navigate to dashboard
    await expect(page).toHaveURL('/', { timeout: 10000 });
  });

  test('shows loading state during submission', async ({ page }) => {
    const user = generateTestUser();
    await registerPage.fillForm(user);

    // Hold the registration request until the loading state has been checked.
    let release!: () => void;
    const released = new Promise<void>((resolve) => {
      release = resolve;
    });
    let held!: () => void;
    const requestHeld = new Promise<void>((resolve) => {
      held = resolve;
    });
    const handler = async (route: Route) => {
      held();
      await released;
      await route.continue();
    };
    await page.route('**/auth/register', handler);

    try {
      await registerPage.submit();
      await requestHeld;

      const loadingButton = page.getByRole('button', { name: 'Creating account...' });
      await expect(loadingButton).toBeVisible();
      await expect(loadingButton).toBeDisabled();
    } finally {
      release();
    }

    await expect(page).toHaveURL('/', LOADED);
    await page.unroute('**/auth/register', handler);
  });

  test('preserves form data on validation error', async () => {
    const user = generateTestUser();
    
    // Fill form with invalid password
    await registerPage.fillForm({
      ...user,
      password: 'short',
      confirmPassword: 'short'
    });
    
    await registerPage.submit();
    await expect(registerPage.fieldError('password')).toContainText('at least 10 characters');
    
    // Check that other fields are preserved
    await expect(registerPage.firstNameInput).toHaveValue(user.firstName);
    await expect(registerPage.lastNameInput).toHaveValue(user.lastName);
    await expect(registerPage.emailInput).toHaveValue(user.email);
  });

  test('can navigate to login page', async ({ page }) => {
    await registerPage.signInLink.click();
    await expect(page).toHaveURL('/login');
  });

  test('clears error messages when field is edited', async () => {
    // First, fill form with invalid data to trigger React Hook Form errors
    await registerPage.firstNameInput.fill('A');
    await registerPage.lastNameInput.fill('B');
    await registerPage.emailInput.fill('invalid-email');
    await registerPage.passwordInput.fill('weak');
    await registerPage.confirmPasswordInput.fill('different');
    
    // Submit to trigger validation
    await registerPage.submit();
    
    // Verify email error is shown
    await expect(registerPage.fieldError('email')).toHaveText('Invalid email address');
    
    // Verify password error is shown
    await expect(registerPage.fieldError('password')).toContainText('at least 10 characters');

    // Verify confirm password error
    await expect(registerPage.fieldError('confirmPassword')).toHaveText("Passwords don't match");
    
    // Fix the email - error should clear
    await registerPage.emailInput.clear();
    await registerPage.emailInput.fill('valid@example.com');
    await expect(registerPage.fieldError('email')).toBeHidden();
    
    // Fix the password - error should clear
    await registerPage.passwordInput.clear();
    await registerPage.passwordInput.fill(testPasswords.allRequirements);
    await expect(registerPage.fieldError('password')).toBeHidden();

    // Fix confirm password - error should clear
    await registerPage.confirmPasswordInput.clear();
    await registerPage.confirmPasswordInput.fill(testPasswords.allRequirements);
    await expect(registerPage.fieldError('confirmPassword')).toBeHidden();
  });

  test('handles network error gracefully', async ({ page, context }) => {
    const user = generateTestUser();
    
    // Block the registration API endpoint
    await context.route('**/auth/register', route => route.abort());
    
    // Fill and submit form
    await registerPage.fillForm(user);
    await registerPage.submit();
    
    // The form shows its own message for a request that never got an answer.
    await expect(page.getByRole('alert')).toHaveText('Registration failed. Please try again.', LOADED);
    await expect(page).toHaveURL(/\/register$/);
  });

  test('successful registration with all valid data', async ({ page }) => {
    const user = generateTestUser();

    await registerPage.fillForm(user);

    const responsePromise = apiResponse(page, /\/auth\/register$/, 'POST');
    await registerPage.submit();

    const response = await responsePromise;
    expect(response.status()).toBe(201);
    const body = await response.json();
    expect(body.success).toBe(true);
    expect(body.data.user.email).toBe(user.email);

    await expect(page).toHaveURL('/', LOADED);
  });
});

// The switch itself: security.allow_public_registration ships disabled, and
// global-setup enables it for the rest of the suite. These tests restore the
// shipped state, verify the closed surface end to end, and reopen it in
// afterAll for the spec files that run later (workers=1 keeps this race-free).
test.describe('Registration disabled (security.allow_public_registration=false)', () => {
  test.beforeAll(async () => {
    await setPublicRegistration(false);
  });

  test.afterAll(async () => {
    await setPublicRegistration(true);
  });

  test('login page offers no sign-up link', async ({ page }) => {
    const statusProbe = page.waitForResponse(
      response =>
        response.url().includes('/auth/registration') && response.request().method() === 'GET'
    );
    await page.goto('/login');
    await statusProbe;

    await expect(page.getByRole('link', { name: /forgot password/i })).toBeVisible();
    await expect(page.getByRole('link', { name: /sign up/i })).toHaveCount(0);
  });

  test('the register page shows the disabled notice instead of the form', async ({ page }) => {
    await page.goto('/register');

    await expect(page.getByText(/registration is disabled/i)).toBeVisible();
    await expect(page.getByLabel(/email address/i)).toHaveCount(0);
    await expect(page.getByRole('link', { name: /back to sign in/i })).toBeVisible();
  });

  test('a direct POST /auth/register is refused with 403', async ({ request }) => {
    const user = generateTestUser();
    const response = await request.post(`${API_BASE_URL}/auth/register`, {
      data: {
        email: user.email,
        password: user.password,
        first_name: user.firstName,
        last_name: user.lastName,
      },
    });

    expect(response.status()).toBe(403);
    const body = await response.json();
    expect(body.success).toBe(false);
  });
});
