import { Page, Locator } from '@playwright/test';

export class RegisterPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators
  get firstNameInput() { 
    return this.page.locator('input[name="first_name"]'); 
  }
  
  get lastNameInput() { 
    return this.page.locator('input[name="last_name"]'); 
  }
  
  get emailInput() { 
    return this.page.locator('input[name="email"]'); 
  }
  
  get passwordInput() { 
    return this.page.locator('input[name="password"]'); 
  }
  
  get confirmPasswordInput() { 
    return this.page.locator('input[name="confirmPassword"]'); 
  }
  
  get submitButton() { 
    return this.page.locator('button[type="submit"]:has-text("Sign Up")'); 
  }
  
  get loadingButton() { 
    return this.page.locator('button:has-text("Creating account...")'); 
  }

  get signInLink() {
    return this.page.locator('a:has-text("Already have an account? Sign In")');
  }

  get passwordVisibilityToggle() {
    return this.page.locator('button[aria-label="toggle password visibility"]').first();
  }

  get confirmPasswordVisibilityToggle() {
    return this.page.locator('button[aria-label="toggle password visibility"]').nth(1);
  }
  
  // Actions
  async goto() {
    await this.page.goto('/register');
    await this.page.waitForLoadState('networkidle');
    // Wait for the form to be ready
    await this.firstNameInput.waitFor({ state: 'visible' });
    await this.submitButton.waitFor({ state: 'visible' });
    // Give React time to fully mount
    await this.page.waitForTimeout(500);
  }

  async fillForm(user: { 
    firstName: string; 
    lastName: string; 
    email: string; 
    password: string; 
    confirmPassword?: string 
  }) {
    await this.firstNameInput.fill(user.firstName);
    await this.lastNameInput.fill(user.lastName);
    await this.emailInput.fill(user.email);
    await this.passwordInput.fill(user.password);
    await this.confirmPasswordInput.fill(user.confirmPassword || user.password);
  }

  async submit() {
    await this.submitButton.click();
  }

  async submitAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/auth/register') && response.status() === 201
    );
    await this.submit();
    return await responsePromise;
  }

  async getErrorMessage(fieldName: string): Promise<string | null> {
    const field = this.page.locator(`input[name="${fieldName}"]`);
    const errorElement = field.locator('xpath=../..').locator('.MuiFormHelperText-root.Mui-error');
    
    try {
      await errorElement.waitFor({ state: 'visible', timeout: 2000 });
      return await errorElement.textContent();
    } catch {
      return null;
    }
  }

  async checkHTML5ValidationMessage(fieldName: string): Promise<string | null> {
    const field = this.page.locator(`input[name="${fieldName}"]`);
    
    // Check if the field has HTML5 validation error
    const validationMessage = await field.evaluate((el: HTMLInputElement) => {
      return el.validationMessage || null;
    });
    
    return validationMessage;
  }

  async isFieldInvalid(fieldName: string): Promise<boolean> {
    const field = this.page.locator(`input[name="${fieldName}"]`);
    
    // Check both HTML5 validity and aria-invalid attribute
    const isInvalid = await field.evaluate((el: HTMLInputElement) => {
      return !el.validity.valid || el.getAttribute('aria-invalid') === 'true';
    });
    
    return isInvalid;
  }

  async getGeneralError(): Promise<string | null> {
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

  async clearForm() {
    await this.firstNameInput.clear();
    await this.lastNameInput.clear();
    await this.emailInput.clear();
    await this.passwordInput.clear();
    await this.confirmPasswordInput.clear();
  }

  async blur(field: Locator) {
    await field.blur();
    await this.page.waitForTimeout(100);
  }
}