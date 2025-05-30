import { Page, Locator } from '@playwright/test';

export class UsersPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Users")');
  }

  get newUserButton() {
    return this.page.locator('button:has-text("New User")');
  }

  get usersTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  get roleFilter() {
    return this.page.locator('select[name="role"]');
  }

  // Locators for form view
  get firstNameInput() {
    return this.page.locator('input[name="firstName"]');
  }

  get lastNameInput() {
    return this.page.locator('input[name="lastName"]');
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

  get roleSelect() {
    return this.page.locator('select[name="role"]');
  }

  get isActiveSwitch() {
    return this.page.locator('input[name="isActive"]');
  }

  get saveButton() {
    return this.page.locator('button:has-text("Save")');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  get deleteButton() {
    return this.page.locator('button:has-text("Delete")');
  }

  get confirmDeleteButton() {
    return this.page.locator('button:has-text("Delete"):visible');
  }

  get deactivateButton() {
    return this.page.locator('button:has-text("Deactivate")');
  }

  get activateButton() {
    return this.page.locator('button:has-text("Activate")');
  }

  // Actions
  async goto() {
    await this.page.goto('/users');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewUser() {
    await this.newUserButton.click();
    await this.page.waitForURL('**/users/new');
  }

  async fillUserForm(userData: {
    firstName: string;
    lastName: string;
    email: string;
    password?: string;
    confirmPassword?: string;
    role?: string;
    isActive?: boolean;
  }) {
    await this.firstNameInput.fill(userData.firstName);
    await this.lastNameInput.fill(userData.lastName);
    await this.emailInput.fill(userData.email);
    
    if (userData.password) {
      await this.passwordInput.fill(userData.password);
    }
    
    if (userData.confirmPassword) {
      await this.confirmPasswordInput.fill(userData.confirmPassword);
    }
    
    if (userData.role) {
      await this.roleSelect.selectOption(userData.role);
    }
    
    if (userData.isActive !== undefined) {
      const isChecked = await this.isActiveSwitch.isChecked();
      if (isChecked !== userData.isActive) {
        await this.isActiveSwitch.click();
      }
    }
  }

  async saveUser() {
    await this.saveButton.click();
    await this.page.waitForURL('**/users/**');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/users') && response.request().method() === 'POST'
    );
    await this.saveUser();
    return await responsePromise;
  }

  async editUser(rowIndex: number = 0) {
    const editButton = this.tableRows.nth(rowIndex).locator('button:has-text("Edit")');
    await editButton.click();
    await this.page.waitForURL('**/users/**/edit');
  }

  async viewUser(rowIndex: number = 0) {
    const viewButton = this.tableRows.nth(rowIndex).locator('button:has-text("View")');
    await viewButton.click();
    await this.page.waitForURL('**/users/**');
  }

  async deleteUser(rowIndex: number = 0) {
    const deleteButton = this.tableRows.nth(rowIndex).locator('button:has-text("Delete")');
    await deleteButton.click();
    
    await this.confirmDeleteButton.waitFor({ state: 'visible' });
    await this.confirmDeleteButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/users') && response.request().method() === 'DELETE'
    );
  }

  async deactivateUser(rowIndex: number = 0) {
    const deactivateButton = this.tableRows.nth(rowIndex).locator('button:has-text("Deactivate")');
    await deactivateButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/users') && response.request().method() === 'PUT'
    );
  }

  async activateUser(rowIndex: number = 0) {
    const activateButton = this.tableRows.nth(rowIndex).locator('button:has-text("Activate")');
    await activateButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/users') && response.request().method() === 'PUT'
    );
  }

  async searchUsers(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByRole(role: string) {
    await this.roleFilter.selectOption(role);
    await this.page.waitForTimeout(500);
  }

  async getUserCount(): Promise<number> {
    await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
    return await this.tableRows.count();
  }

  async getUserData(rowIndex: number = 0): Promise<{
    firstName: string;
    lastName: string;
    email: string;
    role: string;
    status: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');
    
    return {
      firstName: await cells.nth(0).textContent() || '',
      lastName: await cells.nth(1).textContent() || '',
      email: await cells.nth(2).textContent() || '',
      role: await cells.nth(3).textContent() || '',
      status: await cells.nth(4).textContent() || '',
    };
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

  async getSuccessMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    
    try {
      await alert.waitFor({ state: 'visible', timeout: 2000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}