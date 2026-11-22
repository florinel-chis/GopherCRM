import { Locator, Page, expect } from '@playwright/test';

/** The verbs that start the accessible names of DataTable's row buttons. */
type RowActionVerb = 'View' | 'Edit' | 'Delete';

export class TicketsPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Tickets")');
  }

  get newTicketButton() {
    return this.page.locator('button:has-text("Create Ticket")');
  }

  get ticketsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form view — match actual input[name] attributes in TicketForm.tsx
  get titleInput() {
    return this.page.locator('input[name="subject"]');
  }

  get descriptionTextarea() {
    return this.page.locator('textarea[name="description"]');
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

  // Helper for MUI Autocomplete fields (customer, agent)
  async selectAutocompleteOption(label: string, optionText: string) {
    const autocomplete = this.page.locator(`label:has-text("${label}")`).locator('..').locator('input');
    await autocomplete.click();
    await autocomplete.fill(optionText);
    await this.page.waitForTimeout(500);
    const option = this.page.locator('[role="listbox"] [role="option"]').filter({ hasText: optionText }).first();
    await option.click();
    await this.page.waitForTimeout(300);
  }

  // Actions
  async goto() {
    await this.page.goto('/tickets');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewTicket() {
    await this.newTicketButton.click();
    await this.page.waitForURL('**/tickets/new');
    await this.page.waitForLoadState('networkidle');
  }

  async fillTicketForm(ticketData: {
    subject: string;
    description: string;
    priority?: string;
    status?: string;
  }) {
    await this.titleInput.fill(ticketData.subject);
    await this.descriptionTextarea.fill(ticketData.description);

    if (ticketData.priority) {
      await this.selectMuiOption('priority', ticketData.priority);
    }

    if (ticketData.status) {
      await this.selectMuiOption('status', ticketData.status);
    }
  }

  async saveTicket() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tickets') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  /** Saves the edit form and returns the answer to the PUT for that ticket. */
  async saveAndWaitForUpdate(ticketId: number) {
    const responsePromise = this.page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith(`/tickets/${ticketId}`) &&
        response.request().method() === 'PUT'
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
   * (its subject); the list is searched for it and the matching row comes back.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchTickets(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  /**
   * Clicks one of a row's view, edit or delete buttons, found by role and by
   * the accessible name DataTable gives it ("Edit", or "Edit <row label>" when
   * the list passes one) through a retrying expectation: the table can render
   * before its rows paint, and a missing button must fail the test instead of
   * sending the click elsewhere. There is no positional fallback; strict mode
   * fails if a name matches twice.
   */
  private async clickRowAction(row: Locator, verb: RowActionVerb) {
    const button = row.getByRole('button', { name: new RegExp(`^${verb}\\b`) });
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async editTicket(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'Edit');
    await this.page.waitForURL('**/tickets/**/edit');
  }

  async viewTicket(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'View');
    await this.page.waitForURL(/\/tickets\/\d+$/);
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
    return this.page.getByRole('dialog', { name: 'Delete Ticket' });
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

  async deleteTicket(uniqueText: string) {
    await this.clickDeleteOnRow(uniqueText);

    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tickets') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.page.waitForLoadState('networkidle');
  }

  async searchTickets(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByStatus(status: string) {
    const filterSelect = this.page.locator('[role="combobox"]').first();
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${status}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async filterByPriority(priority: string) {
    const filterSelect = this.page.locator('[role="combobox"]').nth(1);
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${priority}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async getTicketCount(): Promise<number> {
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
