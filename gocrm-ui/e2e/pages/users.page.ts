import { Page } from '@playwright/test';

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
    return this.page.locator('button:has-text("Add User")');
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

  // Locators for form view — match actual input[name] attributes in UserForm.tsx
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

  get isActiveSwitch() {
    return this.page.locator('input[name="is_active"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  // Helper method for Material-UI Select components
  async selectMuiOption(fieldName: string, value: string) {
    const selectField = this.page.locator(`[name="${fieldName}"]`).locator('..');
    await selectField.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${value}"]`);
    await option.click();
    await this.page.waitForTimeout(300);
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
    await this.page.waitForLoadState('networkidle');
  }

  async fillUserForm(userData: {
    firstName: string;
    lastName: string;
    email: string;
    password?: string;
    confirmPassword?: string;
    role?: string;
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
      await this.selectMuiOption('role', userData.role);
    }
  }

  async saveUser() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/users') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  async editUser(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const editBtn = row.locator('[data-testid="EditIcon"]').first();
    if (await editBtn.isVisible()) {
      await editBtn.click();
    } else {
      await row.locator('button').nth(1).click();
    }
    await this.page.waitForURL('**/users/**/edit');
  }

  async viewUser(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const viewBtn = row.locator('[data-testid="VisibilityIcon"]').first();
    if (await viewBtn.isVisible()) {
      await viewBtn.click();
    } else {
      await row.locator('button').nth(0).click();
    }
    await this.page.waitForURL('**/users/**');
  }

  async clickDeleteOnRow(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const deleteBtn = row.locator('[data-testid="DeleteIcon"]').first();
    if (await deleteBtn.isVisible()) {
      await deleteBtn.click();
    } else {
      await row.locator('button').last().click();
    }
  }

  async confirmDelete() {
    const dialog = this.page.locator('[role="dialog"]');
    await dialog.waitFor({ state: 'visible' });
    await dialog.locator('button:has-text("Delete")').click();
  }

  async deleteUser(rowIndex: number = 0) {
    await this.clickDeleteOnRow(rowIndex);

    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/users') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();
    await responsePromise;
    await this.page.waitForLoadState('networkidle');
  }

  async searchUsers(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByRole(role: string) {
    // MUI Select filter on list page
    const filterSelect = this.page.locator('[role="combobox"]').first();
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${role}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async getUserCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
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
      await alert.waitFor({ state: 'visible', timeout: 5000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }

  async getSuccessMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    try {
      await alert.waitFor({ state: 'visible', timeout: 5000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}
