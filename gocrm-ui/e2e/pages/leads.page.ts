import { Locator, Page, expect } from '@playwright/test';

/** The verbs that start the accessible names of DataTable's row buttons. */
type RowActionVerb = 'View' | 'Edit' | 'Delete';

export class LeadsPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Leads")');
  }

  get newLeadButton() {
    return this.page.locator('button:has-text("Add Lead")');
  }

  get leadsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form view — match actual input[name] attributes in LeadForm.tsx
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

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  /**
   * Picks an option of one of the form's selects. The select is opened by its
   * accessible name (MUI names the combobox by its label followed by the
   * current value, so the label is matched as a substring), the option is
   * picked from the open listbox, and the listbox closing is the completion signal.
   */
  async selectMuiOption(fieldName: string, value: string) {
    const label = LeadsPage.selectLabels[fieldName] ?? fieldName;
    await this.page.getByRole('combobox', { name: label }).click();
    const listbox = this.page.getByRole('listbox');
    await listbox.locator(`li[data-value="${value}"]`).click();
    await expect(listbox).toBeHidden();
  }

  /** The labels LeadForm gives its selects, keyed by field name. */
  private static readonly selectLabels: Record<string, string> = {
    source: 'Lead Source',
    status: 'Status',
  };

  // Actions
  async goto() {
    await this.page.goto('/leads');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewLead() {
    await this.newLeadButton.click();
    await this.page.waitForURL('**/leads/new');
    await this.page.waitForLoadState('networkidle');
  }

  async fillLeadForm(leadData: {
    companyName: string;
    contactName: string;
    email: string;
    phone?: string;
    source?: string;
    status?: string;
    notes?: string;
  }) {
    await this.companyNameInput.fill(leadData.companyName);
    await this.contactNameInput.fill(leadData.contactName);
    await this.emailInput.fill(leadData.email);

    if (leadData.phone) {
      await this.phoneInput.fill(leadData.phone);
    }

    if (leadData.source) {
      await this.selectMuiOption('source', leadData.source);
    }

    if (leadData.status) {
      await this.selectMuiOption('status', leadData.status);
    }

    if (leadData.notes) {
      await this.notesTextarea.fill(leadData.notes);
    }
  }

  async saveLead() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/leads') && response.request().method() === 'POST'
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
   * (its company name); the list is searched for it and the matching row comes back.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchLeads(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  /**
   * Clicks one of a row's view, edit or delete buttons, found by role and by
   * the accessible name DataTable gives it ("Edit Acme Ltd") through a
   * retrying expectation: the table can render before its rows paint, and a
   * missing button must fail the test instead of sending the click elsewhere.
   * There is no positional fallback; strict mode fails if a name matches twice.
   */
  private async clickRowAction(row: Locator, verb: RowActionVerb) {
    const button = row.getByRole('button', { name: new RegExp(`^${verb}\\b`) });
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async editLead(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'Edit');
    await this.page.waitForURL('**/leads/**/edit');
  }

  async viewLead(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'View');
    await this.page.waitForURL(/\/leads\/\d+$/);
  }

  async clickDeleteOnRow(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'Delete');
  }

  /**
   * The delete confirmation. Addressed by its accessible name, not by
   * `[role="dialog"]`: the navigation Drawer also reports that role, so the
   * bare selector matches two elements and trips strict mode.
   */
  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete Lead' });
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

  /**
   * Deletes the matching lead and waits for the dialog to close.
   *
   * The row count is not the completion signal: the list is paginated, so on
   * a full page the deleted row is replaced by the next record and the count
   * never drops.
   */
  async deleteLead(uniqueText: string) {
    await this.clickDeleteOnRow(uniqueText);

    // Set up response listener before clicking confirm
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/leads') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.deleteDialog.waitFor({ state: 'hidden' });
    await this.page.waitForLoadState('networkidle');
  }

  /** Types the search term; callers assert the narrowed rows with retrying expectations. */
  async searchLeads(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
  }

  async getLeadCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
  }
}
