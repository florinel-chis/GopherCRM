import { Page, Locator } from '@playwright/test';

export class LoginPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators
  get emailInput() { 
    return this.page.locator('input[name="email"]'); 
  }
  
  get passwordInput() { 
    return this.page.locator('input[name="password"]'); 
  }
  
  get submitButton() { 
    return this.page.locator('button[type="submit"]:has-text("Sign In")'); 
  }
  
  get loadingButton() { 
    return this.page.locator('button:has-text("Signing in...")'); 
  }

  get signUpLink() {
    return this.page.locator('a:has-text("Don\'t have an account? Sign Up")');
  }

  get passwordVisibilityToggle() {
    return this.page.locator('button[aria-label="toggle password visibility"]');
  }
  
  // Actions
  async goto() {
    await this.page.goto('/login');
    await this.page.waitForLoadState('networkidle');
    // Wait for the form to be ready
    await this.emailInput.waitFor({ state: 'visible' });
    await this.submitButton.waitFor({ state: 'visible' });
    // Give React time to fully mount
    await this.page.waitForTimeout(500);
  }

  async login(email: string, password: string) {
    await this.emailInput.fill(email);
    await this.passwordInput.fill(password);
    await this.submit();
  }

  async submit() {
    await this.submitButton.click();
  }

  async submitAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/auth/login')
    );
    await this.submit();
    return await responsePromise;
  }

  async getErrorMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    
    try {
      await alert.waitFor({ state: 'visible', timeout: 2000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }

  async waitForLoadingToComplete() {
    // Wait for loading button to appear and disappear
    try {
      await this.loadingButton.waitFor({ state: 'visible', timeout: 1000 });
      await this.loadingButton.waitFor({ state: 'hidden', timeout: 10000 });
    } catch {
      // Loading might be too fast to catch, which is fine
    }
  }

  async isPasswordVisible(): Promise<boolean> {
    const type = await this.passwordInput.getAttribute('type');
    return type === 'text';
  }

  async togglePasswordVisibility() {
    await this.passwordVisibilityToggle.click();
  }
}