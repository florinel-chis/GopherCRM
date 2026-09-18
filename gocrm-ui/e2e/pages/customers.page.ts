import { Page, Locator, expect } from '@playwright/test';

export class CustomersPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Customers")');
  }

  get newCustomerButton() {
    return this.page.locator('button:has-text("Add Customer")');
  }

  get customersTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form — match actual input[name] attributes in CustomerForm.tsx
  get companyNameInput() {
    return this.page.locator('input[name="company_name"]');
  }

  get contactNameInput() {
    return this.page.locator('input[name="contact_name"]');
  }

  get emailInput() {
    return this.page.locator('input[name="email"]');
  }

  get phoneInput() {
    return this.page.locator('input[name="phone"]');
  }

  get industryInput() {
    return this.page.locator('input[name="industry"]');
  }

  get websiteInput() {
    return this.page.locator('input[name="website"]');
  }

  get addressInput() {
    return this.page.locator('input[name="address"]');
  }

  get cityInput() {
    return this.page.locator('input[name="city"]');
  }

  get stateInput() {
    return this.page.locator('input[name="state"]');
  }

  get postalCodeInput() {
    return this.page.locator('input[name="postal_code"]');
  }

  get countryInput() {
    return this.page.locator('input[name="country"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  // Actions
  async goto() {
    await this.page.goto('/customers');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewCustomer() {
    await this.newCustomerButton.click();
    await this.page.waitForURL('**/customers/new');
  }

  async fillCustomerForm(data: {
    companyName: string;
    contactName: string;
    email: string;
    phone?: string;
    industry?: string;
    website?: string;
    address?: string;
    city?: string;
    state?: string;
    postalCode?: string;
    country?: string;
    notes?: string;
  }) {
    await this.companyNameInput.fill(data.companyName);
    await this.contactNameInput.fill(data.contactName);
    await this.emailInput.fill(data.email);
    if (data.phone) await this.phoneInput.fill(data.phone);
    if (data.industry) await this.industryInput.fill(data.industry);
    if (data.website) await this.websiteInput.fill(data.website);
    if (data.address) await this.addressInput.fill(data.address);
    if (data.city) await this.cityInput.fill(data.city);
    if (data.state) await this.stateInput.fill(data.state);
    if (data.postalCode) await this.postalCodeInput.fill(data.postalCode);
    if (data.country) await this.countryInput.fill(data.country);
    if (data.notes) await this.notesTextarea.fill(data.notes);
  }

  async saveCustomer() {
    await this.saveButton.click();
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/customers') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  /**
   * Narrows the list to one customer and returns its row.
   *
   * Row position is NOT stable: the list is paginated and server-sorted, and
   * every run appends rows, so `nth(0)` is whatever customer happens to sort
   * first — not the one the test just created. Tests that act on "their"
   * customer pass a value unique to it (its email) and get the matching row.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchCustomers(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  /**
   * Clicks one of a row's action icons.
   *
   * The icon is addressed through a retrying expectation rather than a
   * one-shot `isVisible()` check: after a `goto()` the table can render before
   * the rows paint, and the non-retrying check used to fall through to a
   * positional-button fallback that clicked the wrong control.
   */
  private async clickRowAction(row: Locator, iconTestId: string) {
    const button = row.locator(`[data-testid="${iconTestId}"]`).first();
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async clickEditOnRow(rowIndex: number = 0) {
    // DataTable has icon buttons for view/edit/delete
    await this.clickRowAction(this.tableRows.nth(rowIndex), 'EditIcon');
    await this.page.waitForURL('**/customers/**/edit');
  }

  /** Edits the customer carrying `uniqueText` (typically its email). */
  async clickEditOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'EditIcon');
    await this.page.waitForURL('**/customers/**/edit');
  }

  /**
   * Opens the detail view for a row.
   *
   * Clicking the row itself is not reliable — the click lands on whichever cell
   * sits under the row centre (checkbox / action buttons), which swallows it —
   * so the row's view (visibility) icon is used, the same mechanism the leads
   * and tasks page objects use.
   */
  async clickViewOnRow(rowIndex: number = 0) {
    await this.clickRowAction(this.tableRows.nth(rowIndex), 'VisibilityIcon');
    await this.page.waitForURL(/\/customers\/\d+$/);
  }

  /** Opens the detail view of the customer carrying `uniqueText`. */
  async clickViewOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'VisibilityIcon');
    await this.page.waitForURL(/\/customers\/\d+$/);
  }

  async clickDeleteOnRow(rowIndex: number = 0) {
    await this.clickRowAction(this.tableRows.nth(rowIndex), 'DeleteIcon');
  }

  /** Opens the delete confirmation for the customer carrying `uniqueText`. */
  async clickDeleteOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'DeleteIcon');
  }

  /**
   * The delete confirmation. Addressed by its accessible name, not by
   * `[role="dialog"]`: the navigation Drawer also reports that role, so the
   * bare selector matches two elements and trips strict mode.
   */
  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete Customer' });
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

  async searchCustomers(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async getRowCount(): Promise<number> {
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
}
