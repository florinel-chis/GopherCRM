import { Locator, Page, expect } from '@playwright/test';

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

  // Shown on the edit form only while the signed-in user changes its own email.
  get currentPasswordInput() {
    return this.page.locator('input[name="current_password"]');
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

  /**
   * Narrows the list to one record and returns its row.
   *
   * The list is paginated and server-sorted and every run adds records, so a
   * row's position says nothing about which record it holds. A test that acts
   * on the record it created passes a value only that record carries
   * (its email); the list is searched for it and the matching row comes back.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchUsers(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  /**
   * Clicks one of a row's action icons, addressed by its test id through a
   * retrying expectation: the table can render before its rows paint, and a
   * missing icon must fail the test instead of sending the click elsewhere.
   */
  private async clickRowAction(row: Locator, iconTestId: string) {
    const button = row.locator(`[data-testid="${iconTestId}"]`).first();
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async editUser(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'EditIcon');
    await this.page.waitForURL('**/users/**/edit');
  }

  async viewUser(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'VisibilityIcon');
    await this.page.waitForURL(/\/users\/\d+$/);
  }

  async clickDeleteOnRow(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'DeleteIcon');
  }

  /**
   * The delete confirmation. Addressed by its accessible name, not by
   * `[role="dialog"]`: the navigation Drawer also reports that role, so the
   * bare selector matches two elements and trips strict mode.
   */
  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete User' });
  }

  async confirmDelete() {
    await this.deleteDialog.waitFor({ state: 'visible' });
    await this.deleteDialog.getByRole('button', { name: 'Delete' }).click();
  }

  async cancelDelete() {
    await this.deleteDialog.waitFor({ state: 'visible' });
    await this.deleteDialog.getByRole('button', { name: 'Cancel' }).click();
    await this.deleteDialog.waitFor({ state: 'hidden' });
  }

  async deleteUser(uniqueText: string) {
    await this.clickDeleteOnRow(uniqueText);

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
