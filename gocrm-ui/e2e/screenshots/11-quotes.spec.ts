import { test, expect, request as playwrightRequest, type APIRequestContext, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';
import { expectedAmountText } from '../pages/deals.page';

/**
 * Documentation captures for products and quotes (FEATURES.md 4d): the product
 * list and form, the quote list, and one quote walked from the deal's Quotes
 * section through a two-line draft, the sent state and the print page to
 * acceptance.
 *
 * The catalogue, a customer, three deals and quotes in every final status are
 * seeded through the API so the lists are populated; the quote that is
 * photographed step by step is created through the UI. The products use fixed
 * names and are created only when missing, so repeated runs against the same
 * database do not pile them up on the very screen being captured. The quote
 * created through the UI is shared through module scope, which is why the file
 * runs serially.
 */
test.describe.configure({ mode: 'serial' });

const AREA = 'quotes';
const CURRENCY = 'EUR';

interface SeedProduct {
  name: string;
  sku: string;
  description: string;
  unit_price_cents: number;
  is_active: boolean;
}

const PRODUCTS: SeedProduct[] = [
  {
    name: 'Onboarding workshop',
    sku: 'SRV-ONB-01',
    description: 'One-day on-site onboarding workshop for up to twelve participants.',
    unit_price_cents: 95000,
    is_active: true,
  },
  {
    name: 'Annual support plan',
    sku: 'SUP-ANN-12',
    description: 'Twelve months of business-hours support with a next-day response target.',
    unit_price_cents: 240000,
    is_active: true,
  },
  {
    name: 'Data migration service',
    sku: 'SRV-MIG-01',
    description: 'Import of contacts, deals and tickets from the previous system.',
    unit_price_cents: 180000,
    is_active: true,
  },
  {
    name: 'Additional user seat',
    sku: 'LIC-SEAT-01',
    description: 'One named user, billed per month.',
    unit_price_cents: 2900,
    is_active: true,
  },
  {
    name: 'Legacy connector',
    sku: 'INT-LEG-01',
    description: 'Connector for the retired on-premises mail server.',
    unit_price_cents: 45000,
    is_active: false,
  },
];

const MAIN_DEAL_TITLE = 'Harbor Logistics CRM rollout';
const MAIN_QUOTE_TITLE = 'CRM rollout: workshop and migration';
// One workshop at 10 % off and the migration service at list price:
// subtotal 950.00 + 1,800.00 = 2,750.00, discount 95.00, total 2,655.00.
const EXPECTED_SUBTOTAL = expectedAmountText('2750.00', CURRENCY);
const EXPECTED_DISCOUNT = expectedAmountText('95.00', CURRENCY);
const EXPECTED_TOTAL = expectedAmountText('2655.00', CURRENCY);

interface Seeded {
  products: Map<string, number>;
  mainDealId: number;
}

let seeded: Seeded | null = null;
let mainQuoteId: number | null = null;

/** A calendar date `days` from today, as the API and the date inputs take it. */
function dateFromToday(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

async function apiLogin(): Promise<{ api: APIRequestContext; headers: Record<string, string> }> {
  const api = await playwrightRequest.newContext();
  const login = await api.post(`${API_BASE_URL}/auth/login`, {
    data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
  });
  expect(login.status()).toBe(200);
  return { api, headers: { Authorization: `Bearer ${(await login.json()).data.token}` } };
}

async function post(api: APIRequestContext, headers: Record<string, string>, path: string, data: unknown) {
  const response = await api.post(`${API_BASE_URL}${path}`, { headers, data });
  expect(response.status(), `POST ${path}`).toBeLessThan(300);
  return (await response.json()).data;
}

/** Creates the catalogue entries that are missing, and returns every product's id by name. */
async function seedProducts(api: APIRequestContext, headers: Record<string, string>) {
  const ids = new Map<string, number>();
  for (const product of PRODUCTS) {
    const found = await api.get(`${API_BASE_URL}/products`, {
      headers,
      params: { search: product.name, limit: 100 },
    });
    expect(found.status()).toBe(200);
    const rows: Array<{ id: number; name: string }> = (await found.json()).data ?? [];
    const existing = rows.find((row) => row.name === product.name);
    if (existing) {
      ids.set(product.name, existing.id);
      continue;
    }
    const created = await post(api, headers, '/products', { ...product, currency: CURRENCY });
    ids.set(product.name, created.id);
  }
  return ids;
}

/**
 * Creates a quote on a deal, adds its lines and walks it through `statuses`
 * (each one an allowed transition from the previous status).
 */
async function seedQuote(
  api: APIRequestContext,
  headers: Record<string, string>,
  quote: { title: string; dealId: number; validUntil: string; notes?: string },
  lines: Array<{ productId: number; quantity: number; discountBp: number }>,
  statuses: string[]
) {
  const created = await post(api, headers, '/quotes', {
    title: quote.title,
    deal_id: quote.dealId,
    currency: CURRENCY,
    valid_until: quote.validUntil,
    notes: quote.notes,
  });
  for (const line of lines) {
    await post(api, headers, `/quotes/${created.id}/lines`, {
      product_id: line.productId,
      quantity: line.quantity,
      discount_bp: line.discountBp,
    });
  }
  for (const status of statuses) {
    await post(api, headers, `/quotes/${created.id}/status`, { status });
  }
  return created.id as number;
}

async function seedData(): Promise<Seeded> {
  const { api, headers } = await apiLogin();
  try {
    const products = await seedProducts(api, headers);
    const product = (name: string) => {
      const id = products.get(name);
      expect(id, name).toBeDefined();
      return id as number;
    };

    // Customer emails are unique, so the address carries the run's timestamp.
    const customer = await post(api, headers, '/customers', {
      first_name: 'Maria',
      last_name: 'Ionescu',
      email: `maria.ionescu.${Date.now()}@example.com`,
      company: 'Harbor Logistics',
      position: 'Operations Director',
      city: 'Constanta',
      country: 'Romania',
    });

    const deal = (title: string, stage: string, amountCents: number, probability: number) =>
      post(api, headers, '/deals', {
        title,
        stage,
        amount_cents: amountCents,
        currency: CURRENCY,
        probability,
        expected_close_date: dateFromToday(45),
        customer_id: customer.id,
        source: 'Referral',
      });
    const mainDeal = await deal(MAIN_DEAL_TITLE, 'proposal', 265500, 40);
    const renewalDeal = await deal('Harbor Logistics support renewal', 'negotiation', 240000, 70);
    const pilotDeal = await deal('Harbor Logistics regional office pilot', 'qualification', 150000, 10);

    // An earlier proposal on the main deal, turned down: the deal page then
    // shows the history of offers next to the accepted quote.
    await seedQuote(
      api,
      headers,
      { title: 'CRM rollout: first proposal', dealId: mainDeal.id, validUntil: dateFromToday(30) },
      [
        { productId: product('Onboarding workshop'), quantity: 2, discountBp: 0 },
        { productId: product('Data migration service'), quantity: 1, discountBp: 0 },
      ],
      ['sent', 'rejected']
    );
    await seedQuote(
      api,
      headers,
      {
        title: 'Support plan renewal',
        dealId: renewalDeal.id,
        validUntil: dateFromToday(21),
        notes: 'Renewal at the current list price.',
      },
      [{ productId: product('Annual support plan'), quantity: 1, discountBp: 500 }],
      ['sent']
    );
    await seedQuote(
      api,
      headers,
      { title: 'Additional seats for the dispatch team', dealId: renewalDeal.id, validUntil: dateFromToday(14) },
      [{ productId: product('Additional user seat'), quantity: 15, discountBp: 0 }],
      ['sent', 'accepted']
    );
    await seedQuote(
      api,
      headers,
      { title: 'Regional office pilot', dealId: pilotDeal.id, validUntil: dateFromToday(30) },
      [
        { productId: product('Additional user seat'), quantity: 5, discountBp: 0 },
        { productId: product('Onboarding workshop'), quantity: 1, discountBp: 2000 },
      ],
      []
    );
    await seedQuote(
      api,
      headers,
      { title: 'Pilot seats, spring offer', dealId: pilotDeal.id, validUntil: dateFromToday(7) },
      [{ productId: product('Additional user seat'), quantity: 5, discountBp: 1500 }],
      ['sent', 'expired']
    );

    return { products, mainDealId: mainDeal.id };
  } finally {
    await api.dispose();
  }
}

async function ensureSeeded(): Promise<Seeded> {
  if (!seeded) {
    seeded = await seedData();
  }
  return seeded;
}

/** The quote created through the UI by the create-dialog capture; the later captures continue it. */
function mainQuote(): number {
  expect(mainQuoteId, 'the 04-quote-create-dialog capture creates the quote').not.toBeNull();
  return mainQuoteId as number;
}

/**
 * Closes the confirmation snackbar a write leaves behind, so it does not cover
 * the lines table in the next capture.
 */
async function dismissSnackbar(page: Page, message: string) {
  const snackbar = page.getByRole('alert').filter({ hasText: message });
  await expect(snackbar).toBeVisible();
  await snackbar.getByRole('button', { name: 'Close' }).click();
  await expect(snackbar).toBeHidden();
}

/** Adds a line through the Add line dialog; `beforeSubmit` runs with the dialog filled in. */
async function addLineThroughDialog(
  page: Page,
  productName: string,
  quantity: string,
  discount: string,
  beforeSubmit?: () => Promise<void>
) {
  const product = PRODUCTS.find((entry) => entry.name === productName);
  expect(product).toBeDefined();
  const optionName = `${productName} (${product?.sku})`;

  await page.getByRole('button', { name: 'Add line' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add line' });
  await expect(dialog).toBeVisible();
  const productInput = dialog.getByRole('combobox', { name: 'Product' });
  await productInput.click();
  await productInput.fill(productName);
  await page.getByRole('option', { name: optionName, exact: true }).click();
  await expect(productInput).toHaveValue(optionName);
  await dialog.locator('input[name="quantity"]').fill(quantity);
  await dialog.locator('input[name="discount"]').fill(discount);

  if (beforeSubmit) {
    await beforeSubmit();
  }

  const added = page.waitForResponse(
    (response) =>
      /\/quotes\/\d+\/lines$/.test(new URL(response.url()).pathname) && response.request().method() === 'POST'
  );
  await dialog.getByRole('button', { name: 'Add' }).click();
  expect((await added).status()).toBe(201);
  await expect(dialog).toBeHidden();
  await dismissSnackbar(page, 'Line added');
}

test.describe('Screenshots - Products and quotes', () => {
  test.beforeAll(async () => {
    await ensureSeeded();
  });

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('01-products-list', async ({ page }) => {
    await page.goto('/products');
    await expect(page.getByRole('heading', { name: 'Products' })).toBeVisible();

    const rows = page.locator('table tbody tr');
    for (const product of PRODUCTS) {
      await expect(rows.filter({ hasText: product.name }).first()).toBeVisible();
    }
    const inactiveRow = rows.filter({ hasText: 'Legacy connector' }).first();
    await expect(inactiveRow.getByTestId('product-active-chip')).toHaveText('Inactive');

    await capture(page, AREA, '01-products-list', { fullPage: true });
  });

  test('02-product-form', async ({ page }) => {
    await page.goto('/products/new');
    await expect(page.getByRole('heading', { name: 'Create New Product' })).toBeVisible();

    await page.locator('input[name="name"]').fill('Custom report pack');
    await page.locator('input[name="sku"]').fill('SRV-REP-05');
    await page
      .locator('textarea[name="description"]')
      .fill('Five custom dashboard reports designed with the customer and delivered within four weeks.');
    await page.locator('input[name="price"]').fill('1250.00');
    await page.locator('input[name="currency"]').fill(CURRENCY);
    await expect(page.locator('input[name="name"]')).toHaveValue('Custom report pack');
    await expect(page.locator('input[name="price"]')).toHaveValue('1250.00');

    // Captured before submitting so the form shows the filled-in state.
    await capture(page, AREA, '02-product-form');
  });

  test('03-quotes-list', async ({ page }) => {
    await page.goto('/quotes');
    await expect(page.getByRole('heading', { name: 'Quotes' })).toBeVisible();

    const rows = page.locator('table tbody tr');
    await expect(rows.filter({ hasText: 'Support plan renewal' }).first()).toBeVisible();
    for (const status of ['Draft', 'Sent', 'Accepted', 'Rejected', 'Expired']) {
      await expect(rows.getByTestId('quote-status-chip').filter({ hasText: status }).first()).toBeVisible();
    }

    await capture(page, AREA, '03-quotes-list', { fullPage: true });
  });

  test('04-quote-create-dialog', async ({ page }) => {
    const { mainDealId } = await ensureSeeded();

    await page.goto(`/deals/${mainDealId}`);
    const quotesSection = page.locator('section[aria-labelledby="deal-quotes-heading"]');
    await expect(quotesSection).toBeVisible();
    await quotesSection.getByRole('button', { name: 'New quote' }).click();

    const dialog = page.getByRole('dialog', { name: 'New quote' });
    await expect(dialog).toBeVisible();
    // The deal option reads "<title> (<customer company>)".
    await expect(dialog.getByRole('combobox', { name: 'Deal' })).toHaveValue(
      `${MAIN_DEAL_TITLE} (Harbor Logistics)`
    );
    await expect(dialog.getByRole('combobox', { name: 'Customer' })).toHaveValue(/Maria Ionescu/);
    await expect(dialog.locator('input[name="currency"]')).toHaveValue(CURRENCY);
    await dialog.locator('input[name="title"]').fill(MAIN_QUOTE_TITLE);
    await dialog.locator('input[name="valid_until"]').fill(dateFromToday(30));
    await dialog
      .locator('textarea[name="notes"]')
      .fill('Workshop and data migration for the head office. Prices exclude VAT.');

    await capture(page, AREA, '04-quote-create-dialog');

    const created = page.waitForResponse(
      (response) => /\/quotes$/.test(new URL(response.url()).pathname) && response.request().method() === 'POST'
    );
    await dialog.getByRole('button', { name: 'Create' }).click();
    expect((await created).status()).toBe(201);
    await page.waitForURL(/\/quotes\/\d+$/);
    mainQuoteId = Number(page.url().match(/\/quotes\/(\d+)$/)?.[1]);
    expect(mainQuoteId).toBeGreaterThan(0);
  });

  test('05-quote-detail-draft and 06-quote-add-line-dialog', async ({ page }) => {
    const quoteId = mainQuote();
    await page.goto(`/quotes/${quoteId}`);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Draft');

    await addLineThroughDialog(page, 'Onboarding workshop', '1', '10');
    await addLineThroughDialog(page, 'Data migration service', '1', '0', async () => {
      const dialog = page.getByRole('dialog', { name: 'Add line' });
      await expect(dialog.locator('input[name="quantity"]')).toHaveValue('1');
      await capture(page, AREA, '06-quote-add-line-dialog');
    });

    await expect(page.getByTestId('quote-line-row')).toHaveCount(2);
    await expect(page.getByTestId('quote-subtotal')).toHaveText(EXPECTED_SUBTOTAL);
    await expect(page.getByTestId('quote-discount')).toHaveText(EXPECTED_DISCOUNT);
    await expect(page.getByTestId('quote-total')).toHaveText(EXPECTED_TOTAL);
    await expect(page.getByRole('button', { name: 'Mark as sent' })).toBeVisible();

    await capture(page, AREA, '05-quote-detail-draft', { fullPage: true });
  });

  test('07-quote-detail-sent', async ({ page }) => {
    const quoteId = mainQuote();
    await page.goto(`/quotes/${quoteId}`);
    await expect(page.getByTestId('quote-line-row')).toHaveCount(2);

    const markedSent = page.waitForResponse(
      (response) =>
        /\/quotes\/\d+\/status$/.test(new URL(response.url()).pathname) && response.request().method() === 'POST'
    );
    await page.getByRole('button', { name: 'Mark as sent' }).click();
    expect((await markedSent).status()).toBe(200);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Sent');
    await dismissSnackbar(page, 'Quote status updated');
    for (const action of ['Accept', 'Reject', 'Mark expired', 'Revise', 'Email quote']) {
      await expect(page.getByRole('button', { name: action })).toBeVisible();
    }
    await expect(page.getByRole('button', { name: 'Add line' })).toHaveCount(0);
    await expect(page.getByTestId('quote-history-row')).toHaveCount(2);

    await capture(page, AREA, '07-quote-detail-sent', { fullPage: true });
  });

  test('08-quote-print and 09-quote-print-media', async ({ page }) => {
    const quoteId = mainQuote();
    await page.goto(`/quotes/${quoteId}/print`);

    await expect(page.getByTestId('quote-print-number')).toHaveText(/^Quote Q-\d{6}$/);
    await expect(page.getByTestId('quote-print-title')).toHaveText(MAIN_QUOTE_TITLE);
    await expect(page.getByTestId('quote-print-line')).toHaveCount(2);
    await expect(page.getByTestId('quote-print-total')).toHaveText(EXPECTED_TOTAL);
    const navigationEntry = page.getByTestId('app-navigation').getByText('Quotes', { exact: true });
    await expect(navigationEntry).toBeVisible();
    await expect(page.getByTestId('app-bar')).toBeVisible();
    await expect(page.getByTestId('quote-print-toolbar')).toBeVisible();

    await capture(page, AREA, '08-quote-print', { fullPage: true });

    // Under print media the application chrome and the page's own toolbar are
    // hidden; only the quote document remains.
    await page.emulateMedia({ media: 'print' });
    await expect(navigationEntry).toBeHidden();
    await expect(page.getByTestId('app-bar')).toBeHidden();
    await expect(page.getByTestId('quote-print-toolbar')).toBeHidden();
    await expect(page.getByTestId('quote-print-number')).toBeVisible();
    await expect(page.getByTestId('quote-print-total')).toBeVisible();

    await capture(page, AREA, '09-quote-print-media', { fullPage: true });

    await page.emulateMedia({ media: null });
    await expect(navigationEntry).toBeVisible();
  });

  test('10-deal-quotes-section', async ({ page }) => {
    const { mainDealId } = await ensureSeeded();
    const quoteId = mainQuote();
    await page.goto(`/quotes/${quoteId}`);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Sent');

    const accepted = page.waitForResponse(
      (response) =>
        /\/quotes\/\d+\/status$/.test(new URL(response.url()).pathname) && response.request().method() === 'POST'
    );
    await page.getByRole('button', { name: 'Accept' }).click();
    expect((await accepted).status()).toBe(200);
    await expect(page.getByTestId('quote-status-chip')).toHaveText('Accepted');

    await page.goto(`/deals/${mainDealId}`);
    const quotesSection = page.locator('section[aria-labelledby="deal-quotes-heading"]');
    await expect(quotesSection).toBeVisible();
    const quoteRow = quotesSection.getByTestId('deal-quote-row').filter({ hasText: MAIN_QUOTE_TITLE });
    await expect(quoteRow).toHaveCount(1);
    await expect(quoteRow.getByTestId('quote-status-chip')).toHaveText('Accepted');
    await expect(quoteRow).toContainText(EXPECTED_TOTAL);
    const rejectedRow = quotesSection.getByTestId('deal-quote-row').filter({ hasText: 'CRM rollout: first proposal' });
    await expect(rejectedRow.getByTestId('quote-status-chip')).toHaveText('Rejected');

    await capture(page, AREA, '10-deal-quotes-section', { fullPage: true });
  });
});
