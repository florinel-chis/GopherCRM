import { Locator, Page } from '@playwright/test';

/**
 * Page object for the label management screen at /labels.
 *
 * The screen carries two different dialogs, both of which report
 * `role="dialog"`: the create/edit editor (a form with a name field, a swatch
 * picker and a free hex field) and the delete confirmation. They are told apart
 * here by content rather than by index so a stray open dialog can never make a
 * spec act on the wrong one.
 */
export class LabelsPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // --- List view -----------------------------------------------------------

  get pageTitle() {
    return this.page.locator('h4:has-text("Labels")');
  }

  get createLabelButton() {
    return this.page.locator('button:has-text("Create Label")');
  }

  get labelsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  /** The row whose name cell carries this label. Names must be run-unique. */
  row(name: string): Locator {
    return this.tableRows.filter({ hasText: name });
  }

  /** The hex value printed next to the colour swatch, e.g. "#D62728". */
  async colorOf(name: string): Promise<string> {
    const text = await this.row(name).locator('td').nth(1).textContent();
    return (text || '').trim();
  }

  /** The task count column, as a number. */
  async taskCountOf(name: string): Promise<number> {
    const text = await this.row(name).locator('td').nth(2).textContent();
    return Number((text || '').trim());
  }

  // --- Editor dialog -------------------------------------------------------

  /**
   * The create/edit dialog. Identified by the hex field, which the delete
   * confirmation does not have.
   */
  get editorDialog(): Locator {
    return this.page
      .locator('[role="dialog"]')
      .filter({ has: this.page.locator('input[name="color"]') });
  }

  get nameInput() {
    return this.editorDialog.locator('input[name="name"]');
  }

  get colorInput() {
    return this.editorDialog.locator('input[name="color"]');
  }

  get editorSubmitButton() {
    return this.editorDialog.locator('button[type="submit"]');
  }

  get editorCancelButton() {
    return this.editorDialog.locator('button:has-text("Cancel")');
  }

  /** The preset swatch button for one palette colour. */
  swatch(color: string): Locator {
    return this.editorDialog.locator(`button[aria-label="Use color ${color}"]`);
  }

  // --- Delete confirmation -------------------------------------------------

  get deleteDialog(): Locator {
    return this.page.getByRole('dialog', { name: 'Delete Label' });
  }

  // --- Actions -------------------------------------------------------------

  async goto() {
    await this.page.goto('/labels');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async openCreateDialog() {
    await this.createLabelButton.click();
    await this.nameInput.waitFor({ state: 'visible' });
  }

  async openEditDialog(name: string) {
    await this.row(name).locator('[data-testid="EditIcon"]').click();
    await this.nameInput.waitFor({ state: 'visible' });
  }

  async openDeleteDialog(name: string) {
    await this.row(name).locator('[data-testid="DeleteIcon"]').click();
    await this.deleteDialog.waitFor({ state: 'visible' });
  }

  /**
   * Fills the editor. A colour that is one of the presets is picked from the
   * swatch row (the way a user would); anything else is typed into the hex
   * field.
   */
  async fillEditor(data: { name?: string; color?: string; useSwatch?: boolean }) {
    if (data.name !== undefined) {
      await this.nameInput.fill(data.name);
    }
    if (data.color !== undefined) {
      if (data.useSwatch) {
        await this.swatch(data.color).click();
      } else {
        await this.colorInput.fill(data.color);
      }
    }
  }

  /** Submits the editor and returns the label write it triggered. */
  async submitEditor(method: 'POST' | 'PUT') {
    const responsePromise = this.page.waitForResponse(
      (response) =>
        /\/labels(\/\d+)?$/.test(new URL(response.url()).pathname) &&
        response.request().method() === method
    );
    await this.editorSubmitButton.click();
    return await responsePromise;
  }

  /** Confirms the delete dialog and returns the DELETE response. */
  async confirmDelete() {
    const responsePromise = this.page.waitForResponse(
      (response) =>
        /\/labels\/\d+$/.test(new URL(response.url()).pathname) &&
        response.request().method() === 'DELETE'
    );
    await this.deleteDialog.getByRole('button', { name: 'Delete' }).click();
    return await responsePromise;
  }

  async cancelDelete() {
    await this.deleteDialog.getByRole('button', { name: 'Cancel' }).click();
    await this.deleteDialog.waitFor({ state: 'hidden' });
  }

  /** Text of the snackbar the page raises after a mutation. */
  async snackbarMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    try {
      await alert.waitFor({ state: 'visible', timeout: 10000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}
