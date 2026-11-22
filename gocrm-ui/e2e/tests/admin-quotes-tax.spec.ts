import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { DealsPage, generateDealData, expectedAmountText, runStamp } from '../pages/deals.page';
import { apiResponse } from '../helpers/api';

/**
 * Quote tax in the web UI (issue #325): a line added with a 21 % tax rate goes
 * out as tax_rate_bp 2100, and the detail and print pages show the line's tax
 * and the net, tax and gross totals the server computed (#324). Editing the
 * rate to 0 and back proves the update body carries it too.
 *
 * This spec stands beside admin-quotes.spec.ts, which runs unchanged.
 */

test.describe('Admin - Quote tax', () => {
  let adminAuth: AdminAuthHelper;
  let dealsPage: DealsPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    dealsPage = new DealsPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin sets a line tax rate and sees line tax, net, tax and gross totals on the detail and print pages', async ({
    page,
  }) => {
    const stamp = runStamp();
    const productName = `E2E Tax Product ${stamp}`;
    const productSku = `TAX-${stamp}`;
    const quoteTitle = `E2E Tax Quote ${stamp}`;
    // 2 × 12.50 with 10 % off: net 22.50. Tax at 21 %, round half up per line:
    // (2250 × 2100 + 5000) / 10000 = 473 cents, gross 27.23.
    const expectedNet = expectedAmountText('22.50', 'EUR');
    const expectedTax = expectedAmountText('4.73', 'EUR');
    const expectedGross = expectedAmountText('27.23', 'EUR');
    const expectedZero = expectedAmountText('0.00', 'EUR');

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

    // 2. A deal in EUR and a quote from its Quotes section.
    const deal = generateDealData(stamp);
    await dealsPage.createDeal(deal);
    const quotesSection = page.locator('section[aria-labelledby="deal-quotes-heading"]');
    await expect(quotesSection).toBeVisible();
    await quotesSection.getByRole('button', { name: 'New quote' }).click();
    const createDialog = page.getByRole('dialog', { name: 'New quote' });
    await expect(createDialog).toBeVisible();
    await expect(createDialog.locator('input[name="currency"]')).toHaveValue('EUR');
    await createDialog.locator('input[name="title"]').fill(quoteTitle);
    const quoteCreated = apiResponse(page, /\/quotes$/, 'POST');
    await createDialog.getByRole('button', { name: 'Create' }).click();
    expect((await quoteCreated).status()).toBe(201);
    await page.waitForURL(/\/quotes\/\d+$/);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Draft');

    // 3. A line with quantity 2, 10 % off and 21 % tax.
    await page.getByRole('button', { name: 'Add line' }).click();
    const addDialog = page.getByRole('dialog', { name: 'Add line' });
    await expect(addDialog).toBeVisible();
    await expect(addDialog.locator('input[name="tax_rate"]')).toHaveValue('0');
    const productInput = addDialog.getByRole('combobox', { name: 'Product' });
    await productInput.click();
    await productInput.fill(stamp);
    await page.getByRole('option', { name: `${productName} (${productSku})`, exact: true }).click();
    await expect(productInput).toHaveValue(`${productName} (${productSku})`);
    await addDialog.locator('input[name="quantity"]').fill('2');
    await addDialog.locator('input[name="discount"]').fill('10');
    await addDialog.locator('input[name="tax_rate"]').fill('21');
    const lineAdded = apiResponse(page, /\/quotes\/\d+\/lines$/, 'POST');
    await addDialog.getByRole('button', { name: 'Add' }).click();
    const addResponse = await lineAdded;
    expect(addResponse.status()).toBe(201);
    expect(addResponse.request().postDataJSON()).toMatchObject({ quantity: 2, discount_bp: 1000, tax_rate_bp: 2100 });
    await expect(addDialog).toBeHidden();

    const lineRow = page.getByTestId('quote-line-row');
    await expect(lineRow).toHaveCount(1);
    await expect(lineRow.getByTestId('quote-line-tax-rate')).toHaveText('21%');
    await expect(lineRow.getByTestId('quote-line-tax')).toHaveText(expectedTax);
    await expect(page.getByTestId('quote-total')).toHaveText(expectedNet);
    await expect(page.getByTestId('quote-tax')).toHaveText(expectedTax);
    await expect(page.getByTestId('quote-gross')).toHaveText(expectedGross);

    // 4. Edit the rate to 0: no tax, gross equals net.
    await page.getByRole('button', { name: 'Edit line 1' }).click();
    let editDialog = page.getByRole('dialog', { name: 'Edit line' });
    await expect(editDialog).toBeVisible();
    await expect(editDialog.locator('input[name="tax_rate"]')).toHaveValue('21');
    await editDialog.locator('input[name="tax_rate"]').fill('0');
    let lineUpdated = apiResponse(page, /\/quotes\/\d+\/lines\/\d+$/, 'PUT');
    await editDialog.getByRole('button', { name: 'Save' }).click();
    let updateResponse = await lineUpdated;
    expect(updateResponse.status()).toBe(200);
    expect(updateResponse.request().postDataJSON()).toMatchObject({ tax_rate_bp: 0 });
    await expect(editDialog).toBeHidden();
    await expect(lineRow.getByTestId('quote-line-tax-rate')).toHaveText('0%');
    await expect(page.getByTestId('quote-tax')).toHaveText(expectedZero);
    await expect(page.getByTestId('quote-gross')).toHaveText(expectedNet);

    // 5. Back to 21 %.
    await page.getByRole('button', { name: 'Edit line 1' }).click();
    editDialog = page.getByRole('dialog', { name: 'Edit line' });
    await expect(editDialog).toBeVisible();
    await editDialog.locator('input[name="tax_rate"]').fill('21');
    lineUpdated = apiResponse(page, /\/quotes\/\d+\/lines\/\d+$/, 'PUT');
    await editDialog.getByRole('button', { name: 'Save' }).click();
    updateResponse = await lineUpdated;
    expect(updateResponse.status()).toBe(200);
    expect(updateResponse.request().postDataJSON()).toMatchObject({ tax_rate_bp: 2100 });
    await expect(editDialog).toBeHidden();
    await expect(page.getByTestId('quote-gross')).toHaveText(expectedGross);

    // 6. Mark as sent; the tax display stays.
    const markedSent = apiResponse(page, /\/quotes\/\d+\/status$/, 'POST');
    await page.getByRole('button', { name: 'Mark as sent' }).click();
    expect((await markedSent).status()).toBe(200);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Sent');
    await expect(page.getByRole('button', { name: 'Edit line 1' })).toHaveCount(0);
    await expect(lineRow.getByTestId('quote-line-tax-rate')).toHaveText('21%');
    await expect(page.getByTestId('quote-tax')).toHaveText(expectedTax);
    await expect(page.getByTestId('quote-gross')).toHaveText(expectedGross);

    // 7. The print page.
    await page.getByRole('link', { name: 'Print' }).click();
    await page.waitForURL(/\/quotes\/\d+\/print$/);
    const printLine = page.getByTestId('quote-print-line');
    await expect(printLine).toHaveCount(1);
    await expect(printLine.getByTestId('quote-print-line-tax-rate')).toHaveText('21%');
    await expect(printLine.getByTestId('quote-print-line-tax')).toHaveText(expectedTax);
    await expect(page.getByTestId('quote-print-total')).toHaveText(expectedNet);
    await expect(page.getByTestId('quote-print-tax')).toHaveText(expectedTax);
    await expect(page.getByTestId('quote-print-gross')).toHaveText(expectedGross);
  });
});
