import { test, expect } from '@playwright/test';
import { capture } from './helpers/capture';
import { LoginPage } from '../pages/login.page';
import { RegisterPage } from '../pages/register.page';
import { generateUserData } from '../fixtures/admin-user';

/**
 * Documentation captures for the unauthenticated screens: sign in, sign up and
 * their validation states. No login helper is used here on purpose — these
 * pages are what a visitor sees before any session exists.
 *
 * The registration form is never submitted: the captures show the filled form
 * and the client-side validation, so no account is created.
 */
test.describe.configure({ mode: 'serial' });

test.describe('Screenshots — authentication', () => {
  test('login page', async ({ page }) => {
    const loginPage = new LoginPage(page);
    await loginPage.goto();

    await expect(page.getByRole('heading', { name: 'Sign In' })).toBeVisible();
    await expect(loginPage.emailInput).toBeVisible();
    await expect(loginPage.passwordInput).toBeVisible();
    await expect(loginPage.submitButton).toBeVisible();

    await capture(page, 'auth', '01-login');
  });

  test('login validation errors', async ({ page }) => {
    const loginPage = new LoginPage(page);
    await loginPage.goto();

    // A non-empty password keeps the browser's own "required" check out of the
    // way, so the zod resolver runs and renders its message inline.
    await loginPage.emailInput.fill('not-an-email');
    await loginPage.passwordInput.fill('wrong');
    await loginPage.submit();

    await expect(page.getByText('Invalid email address')).toBeVisible();
    await expect(page).toHaveURL(/\/login/);

    await capture(page, 'auth', '02-login-validation');
  });

  test('registration page with a filled form', async ({ page }) => {
    const registerPage = new RegisterPage(page);
    await registerPage.goto();

    const applicant = generateUserData();
    await registerPage.fillForm({
      firstName: applicant.firstName,
      lastName: applicant.lastName,
      email: applicant.email,
      password: applicant.password,
    });

    await expect(registerPage.firstNameInput).toHaveValue(applicant.firstName);
    await expect(registerPage.emailInput).toHaveValue(applicant.email);
    await expect(registerPage.confirmPasswordInput).toHaveValue(applicant.password);
    // Deliberately not submitted — the capture documents the form, not a signup.

    await capture(page, 'auth', '03-register');
  });

  test('registration validation errors', async ({ page }) => {
    const registerPage = new RegisterPage(page);
    await registerPage.goto();

    const applicant = generateUserData();
    await registerPage.fillForm({
      firstName: applicant.firstName,
      lastName: applicant.lastName,
      email: applicant.email,
      password: 'short',
      confirmPassword: 'different',
    });
    await registerPage.submit();

    // The password policy (min 10, upper + lower + digit + special) rejects it
    // client-side, so no request reaches the API.
    await expect(page.getByText('Password must be at least 10 characters')).toBeVisible();
    await expect(page).toHaveURL(/\/register/);

    await capture(page, 'auth', '04-register-validation');
  });
});
