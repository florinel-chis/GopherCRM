import { test, expect, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { DealsPage, generateDealData, expectedAmountText, runStamp } from '../pages/deals.page';

/**
 * The admin flow from product to accepted quote (issue #167, AC-10): create a
 * product, open a deal, start a quote from the deal's Quotes section, add a
 * line, mark the quote sent, open the print page (and check the navigation is
 * hidden under print media), accept the quote and see it on the deal.
 *
 * The selectors live here rather than in a page object because this spec is
 * the only e2e change the task allows; a `quotes.page.ts` is the natural next
 * step when a second quotes spec appears.
 */

const apiResponse = (page: Page, pathPattern: RegExp, method: 'POST' | 'PUT' | 'GET') =>
  page.waitForResponse(
    (response) => pathPattern.test(new URL(response.url()).pathname) && response.request().method() === method
  );

test.describe('Admin - Quotes', () => {
  let adminAuth: AdminAuthHelper;
  let dealsPage: DealsPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    dealsPage = new DealsPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin creates a product, quotes a deal from its Quotes section, sends, prints and accepts the quote', async ({
    page,
  }) => {
    // Every list is server-paginated and other runs leave rows behind, so the
    // product, the deal and the quote carry the run stamp in their names.
    const stamp = runStamp();
    const productName = `E2E Product ${stamp}`;
    const productSku = `SKU-${stamp}`;
    const quoteTitle = `E2E Quote ${stamp}`;
    // 2 × 12.50 with 10 % off: subtotal 25.00, discount 2.50, total 22.50.
    const expectedSubtotal = expectedAmountText('25.00', 'EUR');
    const expectedDiscount = expectedAmountText('2.50', 'EUR');
    const expectedTotal = expectedAmountText('22.50', 'EUR');

    // 1. The product, through the admin-only form.
    await page.goto('/products');
    await expect(page.getByRole('heading', { name: 'Products' })).toBeVisible();
    await page.getByRole('button', { name: 'New Product' }).click();
    await page.waitForURL('**/products/new');
    await expect(page.getByRole('heading', { name: 'Create New Product' })).toBeVisible();
    await page.locator('input[name="name"]').fill(productName);
    await page.locator('input[name="sku"]').fill(productSku);
    await page.locator('input[name="price"]').fill('12.50');
    await page.locator('input[name="currency"]').fill('EUR');
    const productCreated = apiResponse(page, /\/products$/, 'POST');
    await page.locator('button[type="submit"]').click();
    expect((await productCreated).status()).toBe(201);
    await page.waitForURL(/\/products$/);
    await page.locator('input[placeholder*="Search products"]').fill(stamp);
    const productRow = page.locator('table tbody tr').filter({ hasText: productName });
    await expect(productRow).toHaveCount(1, { timeout: 10000 });
    await expect(productRow).toContainText(productSku);
    await expect(productRow).toContainText(expectedAmountText('12.50', 'EUR'));
    await expect(productRow.getByTestId('product-active-chip')).toHaveText('Active');

    // 2. A deal in EUR (the quote takes the deal's currency).
    const deal = generateDealData(stamp);
    const dealId = await dealsPage.createDeal(deal);

    // 3. A quote from the deal's Quotes section, with the deal prefilled.
    const quotesSection = page.locator('section[aria-labelledby="deal-quotes-heading"]');
    await expect(quotesSection).toBeVisible();
    await expect(quotesSection).toContainText('No quotes for this deal');
    await quotesSection.getByRole('button', { name: 'New quote' }).click();
    const createDialog = page.getByRole('dialog', { name: 'New quote' });
    await expect(createDialog).toBeVisible();
    await expect(createDialog.getByRole('combobox', { name: 'Deal' })).toHaveValue(deal.title);
    await expect(createDialog.locator('input[name="currency"]')).toHaveValue('EUR');
    await createDialog.locator('input[name="title"]').fill(quoteTitle);
    const quoteCreated = apiResponse(page, /\/quotes$/, 'POST');
    await createDialog.getByRole('button', { name: 'Create' }).click();
    expect((await quoteCreated).status()).toBe(201);
    await page.waitForURL(/\/quotes\/\d+$/);
    const quoteId = Number(page.url().match(/\/quotes\/(\d+)$/)?.[1]);
    expect(quoteId).toBeGreaterThan(0);
    await expect(page.getByTestId('quote-number')).toHaveText(/^Q-\d{6}$/);
    await expect(page.getByTestId('quote-title')).toHaveText(quoteTitle);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Draft');
    await expect(page.getByRole('link', { name: deal.title })).toHaveAttribute('href', `/deals/${dealId}`);
    await expect(page.getByTestId('quote-history-row')).toHaveCount(1);
    await expect(page.getByTestId('quote-history-row').first()).toContainText('Created as Draft');

    // 4. A line: two of the product at 10 % off. The picker offers active
    // products in the quote's currency; the discount goes out in basis points.
    await page.getByRole('button', { name: 'Add line' }).click();
    const lineDialog = page.getByRole('dialog', { name: 'Add line' });
    await expect(lineDialog).toBeVisible();
    const productInput = lineDialog.getByRole('combobox', { name: 'Product' });
    await productInput.click();
    await productInput.fill(stamp);
    await page.getByRole('option', { name: `${productName} (${productSku})`, exact: true }).click();
    await expect(productInput).toHaveValue(`${productName} (${productSku})`);
    await lineDialog.locator('input[name="quantity"]').fill('2');
    await lineDialog.locator('input[name="discount"]').fill('10');
    const lineAdded = apiResponse(page, /\/quotes\/\d+\/lines$/, 'POST');
    await lineDialog.getByRole('button', { name: 'Add' }).click();
    const lineResponse = await lineAdded;
    expect(lineResponse.status()).toBe(201);
    expect(lineResponse.request().postDataJSON()).toMatchObject({ quantity: 2, discount_bp: 1000 });
    await expect(lineDialog).toBeHidden();
    const lineRows = page.getByTestId('quote-line-row');
    await expect(lineRows).toHaveCount(1);
    await expect(lineRows.first()).toContainText(productName);
    await expect(lineRows.first()).toContainText('10%');
    await expect(page.getByTestId('quote-subtotal')).toHaveText(expectedSubtotal);
    await expect(page.getByTestId('quote-discount')).toHaveText(expectedDiscount);
    await expect(page.getByTestId('quote-total')).toHaveText(expectedTotal);

    // 5. Draft → sent. The edit controls disappear with the draft status.
    const markedSent = apiResponse(page, /\/quotes\/\d+\/status$/, 'POST');
    await page.getByRole('button', { name: 'Mark as sent' }).click();
    expect((await markedSent).status()).toBe(200);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Sent');
    await expect(page.getByTestId('quote-history-row')).toHaveCount(2);
    await expect(page.getByTestId('quote-history-row').nth(1)).toContainText('Draft to Sent');
    await expect(page.getByRole('button', { name: 'Add line' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Edit line 1' })).toHaveCount(0);
    await expect(page.getByRole('button', { name: 'Accept' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Email quote' })).toBeVisible();

    // 6. The print page. jsdom cannot evaluate @media rules, so the hiding of
    // the application chrome under print media is asserted here, with
    // Playwright's media emulation, while the quote content stays visible.
    await page.getByRole('link', { name: 'Print' }).click();
    await page.waitForURL(/\/quotes\/\d+\/print$/);
    await expect(page.getByTestId('quote-print-number')).toHaveText(/^Quote Q-\d{6}$/);
    await expect(page.getByTestId('quote-print-title')).toHaveText(quoteTitle);
    await expect(page.getByTestId('quote-print-line')).toHaveCount(1);
    await expect(page.getByTestId('quote-print-total')).toHaveText(expectedTotal);
    const navigationEntry = page.getByTestId('app-navigation').getByText('Quotes', { exact: true });
    await expect(navigationEntry).toBeVisible();
    await expect(page.getByTestId('app-bar')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Print' })).toBeVisible();

    await page.emulateMedia({ media: 'print' });
    await expect(navigationEntry).toBeHidden();
    await expect(page.getByTestId('app-bar')).toBeHidden();
    await expect(page.getByTestId('quote-print-toolbar')).toBeHidden();
    await expect(page.getByTestId('quote-print-number')).toBeVisible();
    await expect(page.getByTestId('quote-print-line')).toBeVisible();
    await expect(page.getByTestId('quote-print-total')).toBeVisible();

    await page.emulateMedia({ media: null });
    await expect(navigationEntry).toBeVisible();

    // 7. Sent → accepted, back on the detail page.
    await page.getByRole('link', { name: 'Back to quote' }).click();
    await page.waitForURL(/\/quotes\/\d+$/);
    const accepted = apiResponse(page, /\/quotes\/\d+\/status$/, 'POST');
    await page.getByRole('button', { name: 'Accept' }).click();
    expect((await accepted).status()).toBe(200);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Accepted');
    await expect(page.getByTestId('quote-history-row')).toHaveCount(3);
    await expect(page.getByTestId('quote-history-row').nth(2)).toContainText('Sent to Accepted');
    await expect(page.getByTestId('quote-status-actions')).toContainText('No further status changes');

    // 8. The deal's Quotes section shows the accepted quote with its total.
    await dealsPage.gotoDeal(dealId);
    const quoteRow = quotesSection.getByTestId('deal-quote-row').filter({ hasText: quoteTitle });
    await expect(quoteRow).toHaveCount(1);
    await expect(quoteRow.getByTestId('quote-status-chip')).toHaveText('Accepted');
    await expect(quoteRow).toContainText(expectedTotal);
    await expect(quoteRow.getByRole('link')).toHaveAttribute('href', `/quotes/${quoteId}`);
    // Accepting a quote never touches its deal.
    await expect(dealsPage.stageChip).toHaveText('Qualification');
  });
});
