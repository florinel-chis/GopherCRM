import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { DealsPage, expectedAmountText, generateDealData, runStamp, type DealStageLabel } from '../pages/deals.page';
import { DealsBoardPage } from '../pages/deals-board.page';
import { DashboardPage } from '../pages/dashboard.page';

/**
 * A near expected close keeps the created cards on the first page of their
 * column (sorted by expected close, 100 per column), ahead of the far-future
 * deals the other specs leave behind.
 */
function soon(): string {
  const date = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000);
  return date.toISOString().slice(0, 10);
}

/** "1234.56" to 123456, the way the API stores the amount the form sent. */
function amountCents(amount: string): number {
  return Math.round(Number(amount) * 100);
}

test.describe('Admin - Deals board', () => {
  let adminAuth: AdminAuthHelper;
  let dealsPage: DealsPage;
  let boardPage: DealsBoardPage;
  let dashboardPage: DashboardPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    dealsPage = new DealsPage(page);
    boardPage = new DealsBoardPage(page);
    dashboardPage = new DashboardPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  const createDeal = async (stage: DealStageLabel) => {
    const deal = { ...generateDealData(runStamp()), stage, expectedCloseDate: soon() };
    await dealsPage.createDeal(deal);
    return deal;
  };

  test('the board shows created deals in their stage columns with counts and totals', async () => {
    // The suite runs on one worker, so nothing else writes deals between the
    // two readings and each euro total grows by exactly the created amount.
    await boardPage.goto();
    const qualificationBefore = await boardPage.totalCentsIn('Qualification', 'EUR');
    const proposalBefore = await boardPage.totalCentsIn('Proposal', 'EUR');

    const first = await createDeal('Qualification');
    const second = await createDeal('Proposal');

    await boardPage.goto();

    await expect(boardPage.cardIn('Qualification', first.title)).toBeVisible();
    await expect(boardPage.cardIn('Proposal', second.title)).toBeVisible();
    await expect(boardPage.cardIn('Qualification', first.title)).toContainText(
      expectedAmountText(first.amount, first.currency)
    );
    // Other deals share the database, so the headers are checked for at least
    // the created ones and for a euro total with its weighted line.
    expect(await boardPage.countIn('Qualification')).toBeGreaterThanOrEqual(1);
    expect(await boardPage.countIn('Proposal')).toBeGreaterThanOrEqual(1);
    await expect(boardPage.columnHeader('Qualification')).toContainText('€');
    await expect(boardPage.columnHeader('Qualification')).toContainText('Weighted');
    await expect(boardPage.columnHeader('Proposal')).toContainText('€');
    expect(await boardPage.totalCentsIn('Qualification', 'EUR')).toBe(qualificationBefore + amountCents(first.amount));
    expect(await boardPage.totalCentsIn('Proposal', 'EUR')).toBe(proposalBefore + amountCents(second.amount));

    // Won and lost start collapsed.
    await expect(boardPage.closedColumnToggle('Won')).toHaveAttribute('aria-expanded', 'false');
    await expect(boardPage.closedColumnToggle('Lost')).toHaveAttribute('aria-expanded', 'false');
  });

  test('moving a card to Negotiation through its menu moves it and updates both headers', async () => {
    const deal = await createDeal('Qualification');

    await boardPage.goto();
    await expect(boardPage.cardIn('Qualification', deal.title)).toBeVisible();
    const qualificationBefore = await boardPage.countIn('Qualification');
    const negotiationBefore = await boardPage.countIn('Negotiation');

    const response = await boardPage.moveCard(deal.title, 'Negotiation');
    expect(response.status()).toBe(200);

    await expect(boardPage.cardIn('Negotiation', deal.title)).toBeVisible();
    await expect(boardPage.cardIn('Qualification', deal.title)).toHaveCount(0);
    await expect(boardPage.columnCount('Negotiation')).toHaveText(
      new RegExp(`^${negotiationBefore + 1} deals?$`)
    );
    await expect(boardPage.columnCount('Qualification')).toHaveText(
      new RegExp(`^${qualificationBefore - 1} deals?$`)
    );
  });

  test('moving a card to Lost asks for a reason and shows it under the expanded Lost column', async ({ page }) => {
    const deal = await createDeal('Proposal');
    const reason = `Budget cut ${runStamp()}`;

    await boardPage.goto();
    await expect(boardPage.cardIn('Proposal', deal.title)).toBeVisible();

    const response = await boardPage.moveCardToLost(deal.title, reason);
    expect(response.status()).toBe(200);
    await expect(boardPage.cardIn('Proposal', deal.title)).toHaveCount(0);

    await boardPage.expand('Lost');
    const card = boardPage.cardIn('Lost', deal.title);
    await expect(card).toBeVisible();

    // The reason is stored with the deal.
    await card.getByRole('link', { name: deal.title }).click();
    await page.waitForURL(/\/deals\/\d+$/);
    await expect(dealsPage.detailHeading(deal.title)).toBeVisible();
    await expect(dealsPage.stageChip).toHaveText('Lost');
    await expect(dealsPage.lostReason).toHaveText(reason);
  });

  test('the dashboard "Won this month" tile counts a deal moved to Won on the board', async () => {
    const deal = await createDeal('Negotiation');
    const before = await dashboardPage.readWonThisMonth();

    await boardPage.goto();
    const response = await boardPage.moveCard(deal.title, 'Won');
    expect(response.status()).toBe(200);
    await boardPage.expand('Won');
    await expect(boardPage.cardIn('Won', deal.title)).toBeVisible();

    const after = await dashboardPage.readWonThisMonth();
    expect(after).toBe(before + 1);
    await expect(dashboardPage.wonThisMonthTile.getByRole('link', { name: 'View the board' })).toHaveAttribute(
      'href',
      '/deals/board'
    );
  });

  test('the list and board toggle round-trips and the choice is remembered', async ({ page }) => {
    await dealsPage.goto();
    await expect(boardPage.viewToggle('List')).toHaveAttribute('aria-pressed', 'true');

    await boardPage.viewToggle('Board').click();
    await page.waitForURL('**/deals/board');
    await expect(boardPage.heading).toBeVisible();
    await expect(boardPage.viewToggle('Board')).toHaveAttribute('aria-pressed', 'true');

    // The plain /deals link (the nav item) now opens the board.
    await page.goto('/deals');
    await page.waitForURL('**/deals/board');
    await expect(boardPage.heading).toBeVisible();

    await boardPage.viewToggle('List').click();
    await page.waitForURL(/\/deals$/);
    await expect(dealsPage.dealsTable).toBeVisible();
    await expect(boardPage.viewToggle('List')).toHaveAttribute('aria-pressed', 'true');

    // And back to the list for good.
    await page.goto('/deals');
    await expect(dealsPage.dealsTable).toBeVisible();
    await expect(boardPage.heading).toHaveCount(0);
    expect(new URL(page.url()).pathname).toBe('/deals');
  });
});
