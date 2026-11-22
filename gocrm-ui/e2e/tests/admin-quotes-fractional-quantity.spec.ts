import { test, expect, type Response } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { DealsPage, generateDealData, expectedAmountText, runStamp } from '../pages/deals.page';
import { apiResponse } from '../helpers/api';

/**
 * Fractional quote line quantities in the web UI (issue #346): the line dialog
 * sends a quantity such as 1.5 or 0.125 as a JSON number, refuses more than
 * three decimals locally, shows the API's error.details.quantity on the
 * Quantity field, and the detail and print pages show the quantity without
 * trailing zeros next to the totals the server computed (#343).
 *
 * This spec stands beside admin-quotes.spec.ts and admin-quotes-tax.spec.ts,
 * which fill whole quantities and run unchanged.
 */

const QUANTITY_ERROR = 'Quantity must be a number from 0.001 to 1,000,000 with at most three decimals';
const QUANTITY_HINT = 'From 0.001 to 1,000,000, up to three decimals';
const API_QUANTITY_MESSAGE = 'quantity must be a number from 0.001 to 1000000 with at most 3 decimal places';

const money = (cents: number) => expectedAmountText((cents / 100).toFixed(2), 'EUR');

interface QuoteBody {
  subtotal_cents: number;
  discount_cents: number;
  total_cents: number;
  lines: Array<{ quantity: number; line_total_cents: number }>;
}

const quoteFrom = async (response: Response): Promise<QuoteBody> => {
  const body = (await response.json()) as { data: QuoteBody };
  return body.data;
};

test.describe('Admin - Quote fractional quantities', () => {
  let adminAuth: AdminAuthHelper;
  let dealsPage: DealsPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    dealsPage = new DealsPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin adds and edits a fractional line quantity and sees it without trailing zeros on the detail and print pages', async ({
    page,
  }) => {
    const stamp = runStamp();
    const productName = `E2E Qty Product ${stamp}`;
    const productSku = `QTY-${stamp}`;
    const quoteTitle = `E2E Qty Quote ${stamp}`;

    // 1. The product.
    await page.goto('/products');
    await expect(page.getByRole('heading', { name: 'Products' })).toBeVisible();
    await page.getByRole('button', { name: 'New Product' }).click();
    await page.waitForURL('**/products/new');
    await page.locator('input[name="name"]').fill(productName);
    await page.locator('input[name="sku"]').fill(productSku);
    await page.locator('input[name="price"]').fill('12.50');
    await page.locator('input[name="currency"]').fill('EUR');
    const productCreated = apiResponse(page, /\/products$/, 'POST');
    await page.locator('button[type="submit"]').click();
    expect((await productCreated).status()).toBe(201);
    await page.waitForURL(/\/products$/);

    // 2. A deal in EUR and a draft quote from its Quotes section.
    const deal = generateDealData(stamp);
    await dealsPage.createDeal(deal);
    const quotesSection = page.locator('section[aria-labelledby="deal-quotes-heading"]');
    await expect(quotesSection).toBeVisible();
    await quotesSection.getByRole('button', { name: 'New quote' }).click();
    const createDialog = page.getByRole('dialog', { name: 'New quote' });
    await expect(createDialog).toBeVisible();
    await createDialog.locator('input[name="title"]').fill(quoteTitle);
    const quoteCreated = apiResponse(page, /\/quotes$/, 'POST');
    await createDialog.getByRole('button', { name: 'Create' }).click();
    expect((await quoteCreated).status()).toBe(201);
    await page.waitForURL(/\/quotes\/\d+$/);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Draft');

    // 3. A line with quantity 1.5; the totals are the server's.
    await page.getByRole('button', { name: 'Add line' }).click();
    const addDialog = page.getByRole('dialog', { name: 'Add line' });
    await expect(addDialog).toBeVisible();
    const addQuantity = addDialog.locator('input[name="quantity"]');
    await expect(addQuantity).toHaveAttribute('type', 'text');
    await expect(addQuantity).toHaveAttribute('inputmode', 'decimal');
    await expect(addDialog.getByText(QUANTITY_HINT)).toBeVisible();
    const productInput = addDialog.getByRole('combobox', { name: 'Product' });
    await productInput.click();
    await productInput.fill(stamp);
    await page.getByRole('option', { name: `${productName} (${productSku})`, exact: true }).click();
    await expect(productInput).toHaveValue(`${productName} (${productSku})`);
    await addQuantity.fill('1.5');
    const lineAdded = apiResponse(page, /\/quotes\/\d+\/lines$/, 'POST');
    await addDialog.getByRole('button', { name: 'Add' }).click();
    const addResponse = await lineAdded;
    expect(addResponse.status()).toBe(201);
    expect(addResponse.request().postDataJSON()).toMatchObject({ quantity: 1.5 });
    const added = await quoteFrom(addResponse);
    expect(added.lines[0].quantity).toBe(1.5);
    await expect(addDialog).toBeHidden();

    const lineRow = page.getByTestId('quote-line-row');
    await expect(lineRow).toHaveCount(1);
    await expect(lineRow.getByTestId('quote-line-quantity')).toHaveText('1.5');
    await expect(lineRow.getByTestId('quote-line-total')).toHaveText(money(added.lines[0].line_total_cents));
    await expect(page.getByTestId('quote-subtotal')).toHaveText(money(added.subtotal_cents));
    await expect(page.getByTestId('quote-discount')).toHaveText(money(added.discount_cents));
    await expect(page.getByTestId('quote-total')).toHaveText(money(added.total_cents));

    // 4. Edit to 0.125: the PUT body carries it, the row shows it.
    await page.getByRole('button', { name: 'Edit line 1' }).click();
    let editDialog = page.getByRole('dialog', { name: 'Edit line' });
    await expect(editDialog).toBeVisible();
    const editQuantity = editDialog.locator('input[name="quantity"]');
    await expect(editQuantity).toHaveValue('1.5');
    await editQuantity.fill('0.125');
    const lineUpdated = apiResponse(page, /\/quotes\/\d+\/lines\/\d+$/, 'PUT');
    await editDialog.getByRole('button', { name: 'Save' }).click();
    const updateResponse = await lineUpdated;
    expect(updateResponse.status()).toBe(200);
    expect(updateResponse.request().postDataJSON()).toMatchObject({ quantity: 0.125 });
    const updated = await quoteFrom(updateResponse);
    await expect(editDialog).toBeHidden();
    await expect(lineRow.getByTestId('quote-line-quantity')).toHaveText('0.125');
    await expect(lineRow.getByTestId('quote-line-total')).toHaveText(money(updated.lines[0].line_total_cents));
    await expect(page.getByTestId('quote-total')).toHaveText(money(updated.total_cents));

    // 5. More than three decimals is refused locally; no request goes out.
    await page.getByRole('button', { name: 'Edit line 1' }).click();
    editDialog = page.getByRole('dialog', { name: 'Edit line' });
    await expect(editDialog).toBeVisible();
    let lineRequests = 0;
    const countLineRequests = (request: { url: () => string; method: () => string }) => {
      if (/\/quotes\/\d+\/lines/.test(new URL(request.url()).pathname) && request.method() !== 'GET') {
        lineRequests += 1;
      }
    };
    page.on('request', countLineRequests);
    await editDialog.locator('input[name="quantity"]').fill('1.2345');
    await editDialog.getByRole('button', { name: 'Save' }).click();
    await expect(editDialog.getByText(QUANTITY_ERROR)).toBeVisible();
    await expect(editDialog.locator('input[name="quantity"]')).toHaveAttribute('aria-invalid', 'true');
    page.off('request', countLineRequests);
    expect(lineRequests).toBe(0);

    // 6. The API's quantity refusal shows on the Quantity field, not in the alert.
    await page.route(/\/quotes\/\d+\/lines\/\d+$/, async (route) => {
      if (route.request().method() !== 'PUT') {
        await route.fallback();
        return;
      }
      await route.fulfill({
        status: 400,
        contentType: 'application/json',
        body: JSON.stringify({
          success: false,
          error: {
            code: 'VALIDATION_ERROR',
            message: 'Validation failed',
            details: { quantity: API_QUANTITY_MESSAGE },
          },
        }),
      });
    });
    await editDialog.locator('input[name="quantity"]').fill('2.5');
    await editDialog.getByRole('button', { name: 'Save' }).click();
    await expect(editDialog.getByText(API_QUANTITY_MESSAGE)).toBeVisible();
    await expect(editDialog.locator('input[name="quantity"]')).toHaveAttribute('aria-invalid', 'true');
    await expect(editDialog.getByTestId('quote-line-error')).toHaveCount(0);
    await page.unroute(/\/quotes\/\d+\/lines\/\d+$/);
    await editDialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(editDialog).toBeHidden();
    await expect(lineRow.getByTestId('quote-line-quantity')).toHaveText('0.125');

    // 7. The print page.
    await page.getByRole('link', { name: 'Print' }).click();
    await page.waitForURL(/\/quotes\/\d+\/print$/);
    const printLine = page.getByTestId('quote-print-line');
    await expect(printLine).toHaveCount(1);
    await expect(printLine.getByTestId('quote-print-line-quantity')).toHaveText('0.125');
    await expect(printLine.getByTestId('quote-print-line-total')).toHaveText(money(updated.lines[0].line_total_cents));
    await expect(page.getByTestId('quote-print-total')).toHaveText(money(updated.total_cents));
  });
});
