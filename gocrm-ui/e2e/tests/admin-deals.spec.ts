import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { DealsPage, generateDealData, expectedAmountText, runStamp } from '../pages/deals.page';
import { CompaniesPage, generateCompanyData } from '../pages/companies.page';

test.describe('Admin - Deals', () => {
  let adminAuth: AdminAuthHelper;
  let dealsPage: DealsPage;
  let companiesPage: CompaniesPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    dealsPage = new DealsPage(page);
    companiesPage = new CompaniesPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view the deals list page', async () => {
    await dealsPage.goto();
    await expect(dealsPage.pageTitle).toBeVisible();
    await expect(dealsPage.newDealButton).toBeVisible();
    await expect(dealsPage.dealsTable).toBeVisible();
    await expect(dealsPage.stageSelect).toBeVisible();
    await expect(dealsPage.openOnlySwitch).toBeVisible();
  });

  test('admin can create a deal for a company and sees the formatted amount on both detail pages', async ({ page }) => {
    const company = generateCompanyData();
    const companyId = await companiesPage.createCompany(company);
    const deal = generateDealData();

    await dealsPage.goto();
    await dealsPage.clickNewDeal();
    await dealsPage.fillDealForm(deal);
    await dealsPage.pickCompany(company.domain, `${company.name} (${company.domain})`);
    const response = await dealsPage.saveAndWaitForResponse('POST');
    expect(response.status()).toBe(201);

    await page.waitForURL(/\/deals\/\d+$/);
    await expect(dealsPage.detailHeading(deal.title)).toBeVisible();
    await expect(dealsPage.stageChip).toHaveText('Qualification');
    await expect(dealsPage.amountValue).toHaveText(expectedAmountText(deal.amount, deal.currency));
    await expect(page.getByText('10%')).toBeVisible();
    await expect(page.getByText('Dec 31, 2099')).toBeVisible();
    await expect(page.getByText(deal.source, { exact: true })).toBeVisible();
    await expect(page.getByText(deal.notes)).toBeVisible();
    await expect(page.getByRole('link', { name: company.name })).toHaveAttribute('href', `/companies/${companyId}`);
    // Creating writes the first history row.
    await expect(dealsPage.historyRows).toHaveCount(1);
    await expect(dealsPage.historyRows.first()).toContainText('Created in Qualification');

    // The company page lists the deal in its Deals section.
    await page.getByRole('link', { name: company.name }).click();
    await page.waitForURL(`**/companies/${companyId}`);
    const dealsSection = page.locator('section[aria-labelledby="company-deals-heading"]');
    await expect(dealsSection.getByRole('link', { name: deal.title })).toBeVisible();
    await expect(dealsSection).toContainText(expectedAmountText(deal.amount, deal.currency));
  });

  test('validation errors keep the form open', async ({ page }) => {
    await dealsPage.goto();
    await dealsPage.clickNewDeal();

    await dealsPage.amountInput.fill('12,5');
    await dealsPage.currencyInput.fill('EU');
    await dealsPage.saveButton.click();

    await expect(page.getByText('Title is required')).toBeVisible();
    await expect(page.getByText('Amount must be a positive number with at most two decimals')).toBeVisible();
    await expect(page.getByText('Currency must be a 3-letter ISO code')).toBeVisible();
    expect(page.url()).toContain('/deals/new');
  });

  test('moving through the open stages grows the history by one row each time', async ({ page }) => {
    const deal = generateDealData();
    await dealsPage.createDeal(deal);
    await expect(dealsPage.historyRows).toHaveCount(1);

    const toProposal = await dealsPage.changeStage('Proposal');
    expect(toProposal.status()).toBe(200);
    await expect(dealsPage.stageChip).toHaveText('Proposal');
    await expect(dealsPage.historyRows).toHaveCount(2);
    await expect(dealsPage.historyRows.nth(1)).toContainText('Qualification to Proposal');
    // The stage default probability applies when none is sent.
    await expect(page.getByText('40%')).toBeVisible();

    const toNegotiation = await dealsPage.changeStage('Negotiation');
    expect(toNegotiation.status()).toBe(200);
    await expect(dealsPage.stageChip).toHaveText('Negotiation');
    await expect(dealsPage.historyRows).toHaveCount(3);
    await expect(dealsPage.historyRows.nth(2)).toContainText('Proposal to Negotiation');
    await expect(page.getByText('70%')).toBeVisible();
    // Still open: nothing closed.
    await expect(dealsPage.closedAt).toHaveCount(0);
  });

  test('marking a deal lost needs a reason, shows closed_at and the reason, and moving back clears them', async ({ page }) => {
    const deal = generateDealData();
    await dealsPage.createDeal(deal);

    await dealsPage.openLostDialog();
    // An empty reason is refused locally and the dialog stays open.
    await dealsPage.lostDialog.getByRole('button', { name: 'Mark as lost' }).click();
    await expect(dealsPage.lostDialog.getByText('A reason is required to mark the deal as lost')).toBeVisible();
    await expect(dealsPage.stageChip).toHaveText('Qualification');

    const reason = `Budget cut ${runStamp()}`;
    const lost = await dealsPage.confirmLost(reason);
    expect(lost.status()).toBe(200);
    await expect(dealsPage.lostDialog).toBeHidden();
    await expect(dealsPage.stageChip).toHaveText('Lost');
    await expect(dealsPage.closedAt).toBeVisible();
    await expect(dealsPage.closedAt).not.toHaveText('—');
    await expect(dealsPage.lostReason).toHaveText(reason);
    await expect(page.getByText('0%')).toBeVisible();
    await expect(dealsPage.historyRows).toHaveCount(2);
    await expect(dealsPage.historyRows.nth(1)).toContainText('Qualification to Lost');

    // Back to an open stage: closed_at and the reason are cleared.
    const reopened = await dealsPage.changeStage('Proposal');
    expect(reopened.status()).toBe(200);
    await expect(dealsPage.stageChip).toHaveText('Proposal');
    await expect(dealsPage.closedAt).toHaveCount(0);
    await expect(dealsPage.lostReason).toHaveCount(0);
    await expect(dealsPage.historyRows).toHaveCount(3);
  });

  test('admin can edit the title and amount', async ({ page }) => {
    const deal = generateDealData();
    await dealsPage.createDeal(deal);

    await dealsPage.goto();
    await dealsPage.clickEditOnRowMatching(deal.title);
    await expect(dealsPage.titleInput).toHaveValue(deal.title);
    await expect(dealsPage.amountInput).toHaveValue('1234.56');

    const newTitle = `${deal.title} v2`;
    await dealsPage.titleInput.fill(newTitle);
    await dealsPage.amountInput.fill('99.9');
    const response = await dealsPage.saveAndWaitForResponse('PUT');
    expect(response.status()).toBe(200);

    await page.waitForURL(/\/deals\/\d+$/);
    await expect(dealsPage.detailHeading(newTitle)).toBeVisible();
    await expect(dealsPage.amountValue).toHaveText(expectedAmountText('99.90', deal.currency));
  });

  test('admin can filter the list by stage', async () => {
    const stamp = runStamp();
    const open = { ...generateDealData(stamp), title: `E2E Deal ${stamp} open` };
    const proposal = { ...generateDealData(stamp), title: `E2E Deal ${stamp} proposal`, stage: 'Proposal' as const };
    await dealsPage.createDeal(open);
    await dealsPage.createDeal(proposal);

    await dealsPage.goto();
    await dealsPage.searchDeals(stamp);
    await expect(dealsPage.tableRows).toHaveCount(2);

    await dealsPage.filterByStage('Proposal');
    await expect(dealsPage.tableRows).toHaveCount(1);
    await expect(dealsPage.tableRows.first()).toContainText(proposal.title);
    await expect(dealsPage.tableRows.first().getByTestId('deal-stage-chip')).toHaveText('Proposal');

    await dealsPage.filterByStage('All stages');
    await expect(dealsPage.tableRows).toHaveCount(2);
  });

  test('admin can delete a deal', async () => {
    const deal = generateDealData();
    await dealsPage.createDeal(deal);

    await dealsPage.goto();
    await dealsPage.clickDeleteOnRowMatching(deal.title);
    await dealsPage.confirmDelete();
    // The list is still narrowed to the title, so the row must disappear.
    await expect(dealsPage.tableRows.filter({ hasText: deal.title })).toHaveCount(0);
  });
});
