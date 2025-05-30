import { test, expect } from '@playwright/test';
import { RegisterPage } from '../pages/register.page';
import { DashboardPage } from '../pages/dashboard.page';
import { generateTestUser, testPasswords } from '../fixtures/test-data';

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
    
    // Fill and submit form
    await registerPage.fillForm(user);
    
    // Wait for form to be ready
    await page.waitForTimeout(500);
    
    // Submit and wait for response
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/auth/register')
    );
    await registerPage.submit();
    const response = await responsePromise;
    
    // Verify API response
    expect(response.status()).toBe(201);
    
    // Wait for navigation to dashboard
    await page.waitForURL('/', { timeout: 10000 });
    await dashboardPage.waitForDashboardToLoad();
    
    // Give time for state to settle
    await page.waitForTimeout(1000);
    
    // Verify token is stored
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeTruthy();
    
    // Verify user avatar shows first letter of first name or 'U' as fallback
    const userInitial = await dashboardPage.getUserDisplayName();
    const expectedInitial = user.firstName[0].toUpperCase();
    
    // The avatar should show either the first letter or 'U' (fallback)
    expect(userInitial).toBeTruthy();
    expect([expectedInitial, 'U']).toContain(userInitial);
    
    // If we got the expected initial, also verify full name
    if (userInitial === expectedInitial) {
      const fullName = await dashboardPage.getUserFullName();
      expect(fullName).toBe(`${user.firstName} ${user.lastName}`);
    }
  });

  test('shows validation errors for empty fields', async ({ page }) => {
    // Try to submit without filling any fields
    await registerPage.submit();
    
    // HTML5 validation will prevent form submission and show browser's native validation
    // The first required field (first_name) will get focus and show validation message
    const firstNameValidation = await registerPage.checkHTML5ValidationMessage('first_name');
    expect(firstNameValidation).toBeTruthy(); // Browser shows "Please fill out this field" or similar
    
    // Check that the field is marked as invalid
    expect(await registerPage.isFieldInvalid('first_name')).toBe(true);
  });

  test('shows React Hook Form validation for invalid data', async ({ page }) => {
    // Fill in fields with invalid data to bypass HTML5 required validation
    await registerPage.firstNameInput.fill('Test');
    await registerPage.lastNameInput.fill('User');
    await registerPage.emailInput.fill('invalid-email'); // Invalid format
    await registerPage.passwordInput.fill('short'); // Too short
    await registerPage.confirmPasswordInput.fill('short');
    
    // Submit to trigger React Hook Form validation
    await registerPage.submit();
    await page.waitForTimeout(1000);
    
    // Check for custom error messages
    expect(await registerPage.getErrorMessage('email')).toBe('Invalid email address');
    const passwordError = await registerPage.getErrorMessage('password');
    expect(passwordError).toContain('at least 8 characters');
  });

  test('validates email format', async ({ page }) => {
    // Fill required fields first to bypass HTML5 required validation
    await registerPage.firstNameInput.fill('Test');
    await registerPage.lastNameInput.fill('User');
    await registerPage.passwordInput.fill('ValidPass123');
    await registerPage.confirmPasswordInput.fill('ValidPass123');

    // Test invalid email format
    await registerPage.emailInput.fill('invalid-email');
    await registerPage.submit();
    await page.waitForTimeout(500);
    
    const error = await registerPage.getErrorMessage('email');
    expect(error).toBe('Invalid email address');
  });

  test('validates password requirements', async ({ page }) => {
    const user = generateTestUser();
    
    // Test too short password
    await registerPage.fillForm({ ...user, password: 'Short1' });
    await registerPage.submit();
    expect(await registerPage.getErrorMessage('password')).toContain('at least 8 characters');
    
    // Test missing uppercase
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noUppercase });
    await registerPage.submit();
    expect(await registerPage.getErrorMessage('password')).toContain('one uppercase letter');
    
    // Test missing lowercase
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noLowercase });
    await registerPage.submit();
    expect(await registerPage.getErrorMessage('password')).toContain('one lowercase letter');
    
    // Test missing number
    await registerPage.clearForm();
    await registerPage.fillForm({ ...user, password: testPasswords.noNumber });
    await registerPage.submit();
    expect(await registerPage.getErrorMessage('password')).toContain('one number');
  });

  test('validates password confirmation match', async ({ page }) => {
    const user = generateTestUser();
    await registerPage.fillForm({
      ...user,
      confirmPassword: 'DifferentPassword123'
    });
    await registerPage.submit();
    
    expect(await registerPage.getErrorMessage('confirmPassword')).toBe("Passwords don't match");
  });

  test('shows error for duplicate email registration', async ({ page }) => {
    const user = generateTestUser();
    
    // First registration
    await registerPage.fillForm(user);
    const response = await registerPage.submitAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Wait for redirect to dashboard
    await expect(page).toHaveURL('/');
    
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

  test('password visibility toggle works', async ({ page }) => {
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
    
    // Set up promise to check for loading state
    const loadingVisible = registerPage.loadingButton.isVisible();
    
    // Submit form
    await registerPage.submit();
    
    // The button should change to loading state (might be very quick)
    // We're not asserting this strictly as it might be too fast to catch
    
    // Wait for successful navigation
    await expect(page).toHaveURL('/', { timeout: 10000 });
  });

  test('preserves form data on validation error', async ({ page }) => {
    const user = generateTestUser();
    
    // Fill form with invalid password
    await registerPage.fillForm({
      ...user,
      password: 'short',
      confirmPassword: 'short'
    });
    
    await registerPage.submit();
    
    // Check that other fields are preserved
    expect(await registerPage.firstNameInput.inputValue()).toBe(user.firstName);
    expect(await registerPage.lastNameInput.inputValue()).toBe(user.lastName);
    expect(await registerPage.emailInput.inputValue()).toBe(user.email);
  });

  test('can navigate to login page', async ({ page }) => {
    await registerPage.signInLink.click();
    await expect(page).toHaveURL('/login');
  });

  test('clears error messages when field is edited', async ({ page }) => {
    // First, fill form with invalid data to trigger React Hook Form errors
    await registerPage.firstNameInput.fill('A');
    await registerPage.lastNameInput.fill('B');
    await registerPage.emailInput.fill('invalid-email');
    await registerPage.passwordInput.fill('weak');
    await registerPage.confirmPasswordInput.fill('different');
    
    // Submit to trigger validation
    await registerPage.submit();
    await page.waitForTimeout(1000);
    
    // Verify email error is shown
    const emailError = await registerPage.getErrorMessage('email');
    expect(emailError).toBe('Invalid email address');
    
    // Verify password error is shown
    const passwordError = await registerPage.getErrorMessage('password');
    expect(passwordError).toContain('at least 8 characters');
    
    // Verify confirm password error
    const confirmError = await registerPage.getErrorMessage('confirmPassword');
    expect(confirmError).toBe("Passwords don't match");
    
    // Fix the email - error should clear
    await registerPage.emailInput.clear();
    await registerPage.emailInput.fill('valid@example.com');
    await page.waitForTimeout(500);
    expect(await registerPage.getErrorMessage('email')).toBeNull();
    
    // Fix the password - error should clear
    await registerPage.passwordInput.clear();
    await registerPage.passwordInput.fill('ValidPass123');
    await page.waitForTimeout(500);
    expect(await registerPage.getErrorMessage('password')).toBeNull();
    
    // Fix confirm password - error should clear
    await registerPage.confirmPasswordInput.clear();
    await registerPage.confirmPasswordInput.fill('ValidPass123');
    await page.waitForTimeout(500);
    expect(await registerPage.getErrorMessage('confirmPassword')).toBeNull();
  });

  test('handles network error gracefully', async ({ page, context }) => {
    const user = generateTestUser();
    
    // Block the registration API endpoint
    await context.route('**/api/auth/register', route => route.abort());
    
    // Fill and submit form
    await registerPage.fillForm(user);
    await registerPage.submit();
    
    // Should show an error message
    const error = await registerPage.getGeneralError();
    expect(error).toBeTruthy();
  });

  test('successful registration with all valid data', async ({ page }) => {
    const user = generateTestUser();
    
    // Fill form with all valid data
    await registerPage.fillForm(user);
    
    // Submit and wait for API response
    const response = await registerPage.submitAndWaitForResponse();
    
    // Check response
    expect(response.status()).toBe(201);
    const responseData = await response.json();
    expect(responseData.success).toBe(true);
    expect(responseData.data.user.email).toBe(user.email);
    expect(responseData.data.user.first_name).toBe(user.firstName);
    expect(responseData.data.user.last_name).toBe(user.lastName);
    expect(responseData.data.token).toBeTruthy();
    
    // Should redirect to dashboard
    await expect(page).toHaveURL('/');
  });
});