import { Page, Locator, Response, expect } from '@playwright/test';

export type DealStageLabel = 'Qualification' | 'Proposal' | 'Negotiation' | 'Won' | 'Lost';

export interface DealFormData {
  title: string;
  stage?: DealStageLabel;
  /** Decimal text as typed, e.g. "1234.56"; the API stores cents. */
  amount?: string;
  currency?: string;
  probability?: string;
  /** YYYY-MM-DD, what the native date input accepts. */
  expectedCloseDate?: string;
  source?: string;
  notes?: string;
}

/** A stamp unique to one run; letters, digits and hyphens only, so it is safe in hostnames too. */
export function runStamp(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
}

/**
 * Values unique to one run. The title is what `rowMatching` searches for; the
 * stamp keeps it unique among the deals earlier runs left behind.
 */
export function generateDealData(stamp: string = runStamp()): Required<DealFormData> {
  return {
    title: `E2E Deal ${stamp}`,
    stage: 'Qualification',
    amount: '1234.56',
    currency: 'EUR',
    probability: '10',
    expectedCloseDate: '2099-12-31',
    source: `Source ${stamp}`,
    notes: `Created by the e2e suite (${stamp})`,
  };
}

/** The amount as the detail page renders it in the browser's locale (Chromium defaults to en-US). */
export function expectedAmountText(amount: string, currency: string): string {
  return new Intl.NumberFormat('en-US', { style: 'currency', currency }).format(Number(amount));
}

export class DealsPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // List view
  get pageTitle() {
    return this.page.locator('h4:has-text("Deals")');
  }

  get newDealButton() {
    return this.page.getByRole('button', { name: 'New Deal' });
  }

  get dealsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search deals"]');
  }

  /**
   * The stage filter and the form's stage select are both MUI Selects
   * labelled "Stage"; their accessible name is "Stage <current value>", hence
   * the prefix match. Addressed by role: once open, the listbox shares the
   * label and `getByLabel` would match two elements.
   */
  get stageSelect() {
    return this.page.getByRole('combobox', { name: /^Stage/ });
  }

  get openOnlySwitch() {
    return this.page.getByLabel('Open only');
  }

  // Form — match the input[name] attributes in DealForm.tsx
  get titleInput() {
    return this.page.locator('input[name="title"]');
  }

  get amountInput() {
    return this.page.locator('input[name="amount"]');
  }

  get currencyInput() {
    return this.page.locator('input[name="currency"]');
  }

  get probabilityInput() {
    return this.page.locator('input[name="probability"]');
  }

  get expectedCloseInput() {
    return this.page.locator('input[name="expected_close_date"]');
  }

  get lostReasonInput() {
    return this.page.locator('input[name="lost_reason"]');
  }

  get sourceInput() {
    return this.page.locator('input[name="source"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.getByRole('button', { name: 'Cancel' });
  }

  // Detail view
  detailHeading(title: string) {
    return this.page.getByRole('heading', { level: 4, name: title });
  }

  get stageChip() {
    return this.page.getByTestId('deal-stage-chip');
  }

  get amountValue() {
    return this.page.getByTestId('deal-amount');
  }

  get closedAt() {
    return this.page.getByTestId('deal-closed-at');
  }

  get lostReason() {
    return this.page.getByTestId('deal-lost-reason');
  }

  get historySection() {
    return this.page.locator('section[aria-labelledby="deal-history-heading"]');
  }

  get historyRows() {
    return this.historySection.getByTestId('deal-history-row');
  }

  get detailEditButton() {
    return this.page.getByRole('button', { name: 'Edit' });
  }

  get detailDeleteButton() {
    return this.page.getByRole('button', { name: 'Delete deal' });
  }

  /** Addressed by name: the navigation Drawer also reports role dialog. */
  get lostDialog() {
    return this.page.getByRole('dialog', { name: 'Mark deal as lost' });
  }

  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete Deal' });
  }

  // Actions
  async goto() {
    await this.page.goto('/deals');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewDeal() {
    await this.newDealButton.click();
    await this.page.waitForURL('**/deals/new');
  }

  async pickStage(label: DealStageLabel) {
    await this.stageSelect.click();
    await this.page.getByRole('option', { name: label, exact: true }).click();
  }

  async fillDealForm(data: DealFormData) {
    await this.titleInput.fill(data.title);
    if (data.stage) await this.pickStage(data.stage);
    if (data.amount !== undefined) await this.amountInput.fill(data.amount);
    if (data.currency !== undefined) await this.currencyInput.fill(data.currency);
    if (data.probability !== undefined) await this.probabilityInput.fill(data.probability);
    if (data.expectedCloseDate !== undefined) await this.expectedCloseInput.fill(data.expectedCloseDate);
    if (data.source !== undefined) await this.sourceInput.fill(data.source);
    if (data.notes !== undefined) await this.notesTextarea.fill(data.notes);
  }

  /**
   * Picks a company in the deal form's "Company" autocomplete: type a fragment
   * (the domain is unique), then click the option, which reads "Name (domain)".
   */
  async pickCompany(typed: string, optionLabel: string) {
    const input = this.page.getByRole('combobox', { name: 'Company', exact: true });
    await input.click();
    await input.fill(typed);
    await this.page.getByRole('option', { name: optionLabel, exact: true }).click();
    await expect(input).toHaveValue(optionLabel);
  }

  /** Submits the form and returns the POST or PUT /deals response. */
  async saveAndWaitForResponse(method: 'POST' | 'PUT' = 'POST') {
    const responsePromise = this.page.waitForResponse(
      (response) =>
        /\/deals(\/\d+)?$/.test(new URL(response.url()).pathname) &&
        response.request().method() === method
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  /** Creates a deal through the UI and returns its id from the detail URL. */
  async createDeal(data: DealFormData, companyPick?: { typed: string; optionLabel: string }): Promise<number> {
    await this.goto();
    await this.clickNewDeal();
    await this.fillDealForm(data);
    if (companyPick) {
      await this.pickCompany(companyPick.typed, companyPick.optionLabel);
    }
    const response = await this.saveAndWaitForResponse('POST');
    expect(response.status()).toBe(201);
    await this.page.waitForURL(/\/deals\/\d+$/);
    // The router pushes the new URL before React commits the detail route (the
    // state update runs in a transition), so for a moment the URL already reads
    // /deals/:id while the form, with its own "Stage" select, is still mounted.
    // A stage pick in that window opens the form's menu, which the route swap
    // then removes. Wait for the detail page itself: its heading, its stage
    // chip, and the creation row of the history.
    await expect(this.detailHeading(data.title)).toBeVisible();
    await expect(this.stageChip).toHaveText(data.stage ?? 'Qualification');
    await expect(this.historyRows).toHaveCount(1);
    const match = this.page.url().match(/\/deals\/(\d+)$/);
    return Number(match?.[1]);
  }

  async gotoDeal(id: number) {
    await this.page.goto(`/deals/${id}`);
    await this.page.waitForLoadState('networkidle');
    await this.stageChip.waitFor({ state: 'visible' });
  }

  /**
   * Moves the deal on its detail page to an open stage or to won, and waits
   * for the POST /deals/:id/stage response. Lost goes through `markLost`.
   */
  async changeStage(label: Exclude<DealStageLabel, 'Lost'>) {
    const responsePromise = this.stageResponse();
    const historyPromise = this.historyResponse();
    await this.pickStage(label);
    const response = await responsePromise;
    await this.settleAfterStageChange(response, historyPromise, label);
    return response;
  }

  /** Opens the lost dialog from the stage select; does not confirm. */
  async openLostDialog() {
    await this.pickStage('Lost');
    await this.lostDialog.waitFor({ state: 'visible' });
  }

  /** Confirms the lost dialog with `reason` and waits for the stage response. */
  async confirmLost(reason: string) {
    await this.lostDialog.getByLabel(/^Lost reason/).fill(reason);
    const responsePromise = this.stageResponse();
    const historyPromise = this.historyResponse();
    await this.lostDialog.getByRole('button', { name: 'Mark as lost' }).click();
    const response = await responsePromise;
    await this.settleAfterStageChange(response, historyPromise, 'Lost');
    return response;
  }

  private stageResponse() {
    return this.page.waitForResponse(
      (response) =>
        /\/deals\/\d+\/stage$/.test(new URL(response.url()).pathname) &&
        response.request().method() === 'POST'
    );
  }

  private historyResponse() {
    return this.page.waitForResponse(
      (response) =>
        /\/deals\/\d+\/history$/.test(new URL(response.url()).pathname) &&
        response.request().method() === 'GET'
    );
  }

  /**
   * After a successful stage change the page stores the returned deal and
   * refetches the history. Waits for that refetch, the chip showing the new
   * stage and the select enabled again, so the next stage pick starts from a
   * settled page. A failed change triggers no refetch; the caller asserts the
   * status, so nothing is awaited then.
   */
  private async settleAfterStageChange(
    response: Response,
    historyPromise: Promise<Response>,
    label: DealStageLabel
  ) {
    if (!response.ok()) {
      historyPromise.catch(() => undefined);
      return;
    }
    await historyPromise;
    await expect(this.stageChip).toHaveText(label);
    await expect(this.stageSelect).toBeEnabled();
  }

  /**
   * Narrows the list to one deal and returns its row.
   *
   * Row position is not stable: the list is paginated and server-sorted, and
   * every run appends rows. Tests pass the deal's title, which carries the
   * run stamp, and get the matching row.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchDeals(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  private async clickRowAction(row: Locator, iconTestId: string) {
    const button = row.locator(`[data-testid="${iconTestId}"]`).first();
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async clickEditOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'EditIcon');
    await this.page.waitForURL('**/deals/**/edit');
  }

  async clickViewOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'VisibilityIcon');
    await this.page.waitForURL(/\/deals\/\d+$/);
  }

  async clickDeleteOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'DeleteIcon');
  }

  async confirmDelete() {
    await this.deleteDialog.waitFor({ state: 'visible' });
    await this.deleteDialog.getByRole('button', { name: 'Delete' }).click();
  }

  async searchDeals(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  /**
   * Applies the stage filter on the list page and waits until the table shows
   * rows of that stage only. This is deliberately not a network wait: the list
   * query is keyed on its filters and cached for five minutes, so going back to
   * a combination already fetched (say "All stages" after a stage) renders from
   * the cache without a request, and a `waitForResponse` would time out.
   * Callers assert the resulting row count and contents themselves.
   */
  async filterByStage(label: DealStageLabel | 'All stages') {
    await this.stageSelect.click();
    await this.page.getByRole('option', { name: label, exact: true }).click();
    await expect(this.stageSelect).toHaveText(label);
    if (label !== 'All stages') {
      const chips = this.page.getByTestId('deal-stage-chip');
      const rowsInAnotherStage = this.tableRows
        .filter({ has: chips })
        .filter({ hasNot: chips.filter({ hasText: label }) });
      await expect(rowsInAnotherStage).toHaveCount(0);
    }
  }
}
