import { execFileSync } from 'child_process';
import path from 'path';
import { fileURLToPath } from 'url';
import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';

/**
 * Documentation capture of the public quote response page (docs/reference/features.md 4d):
 * what a client sees on opening the link of a quote email, before answering.
 *
 * A customer, a product and a sent quote are seeded through the API with fixed
 * names, created only when missing; a sent quote of that title is reused.
 * The link exists only in the mail body, so `scripts/e2e/quote-response-link`
 * emails the quote through the real quote service with a capturing mailer and
 * prints it; the command runs only under scripts/e2e/run.sh. The link is a
 * live credential: it is never logged and never part of a test title or an
 * assertion message. The page drops the token from the address bar before the
 * capture, and nothing is accepted or declined.
 */

const repoRoot = path.resolve(fileURLToPath(new URL('.', import.meta.url)), '..', '..', '..');
const LINK_MARKER = '/quote-response?token=';
const LOADED = { timeout: 30 * 1000 };
const CURRENCY = 'EUR';

const CUSTOMER_EMAIL = 'mihai.georgescu@northwind-bakery.example';
const PRODUCT = {
  name: 'Annual support plan',
  sku: 'SUP-ANN-12',
  description: 'Twelve months of business-hours support with a next-day response target.',
  unit_price_cents: 240000,
  is_active: true,
};
const SEAT_PRODUCT = {
  name: 'Additional user seat',
  sku: 'LIC-SEAT-01',
  description: 'One named user, billed per month.',
  unit_price_cents: 2900,
  is_active: true,
};
const QUOTE_TITLE = 'Northwind Bakery support and seats';

// The path and query of the link, to open on the UI under test.
const responseLinkPath = (quoteId: number): string => {
  const output = execFileSync('go', ['run', './scripts/e2e/quote-response-link', '-quote-id', String(quoteId)], {
    cwd: repoRoot,
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf8',
  });
  const line = output
    .split('\n')
    .map((value) => value.trim())
    .filter((value) => value.includes(LINK_MARKER))
    .pop();
  if (!line) {
    throw new Error('quote-response-link printed no response link');
  }
  const url = new URL(line);
  return `${url.pathname}${url.search}`;
};

/** A list answer's rows: a bare array, or the one array inside a wrapper such as `{ leads: [...] }`. */
function rowsOf<T>(data: unknown): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === 'object') {
    const inner = Object.values(data).find(Array.isArray);
    if (inner) return inner as T[];
  }
  return [];
}

async function get<T>(api: APIRequestContext, headers: Record<string, string>, apiPath: string, params = {}) {
  const response = await api.get(`${API_BASE_URL}${apiPath}`, { headers, params });
  expect(response.status(), `GET ${apiPath}`).toBe(200);
  return rowsOf<T>((await response.json()).data);
}

async function post(api: APIRequestContext, headers: Record<string, string>, apiPath: string, data: unknown) {
  const response = await api.post(`${API_BASE_URL}${apiPath}`, { headers, data });
  expect(response.status(), `POST ${apiPath}`).toBeLessThan(300);
  return (await response.json()).data;
}

/** Returns the id and number of a sent quote with the fixed title, creating it when missing. */
async function ensureSentQuote(): Promise<{ id: number; number: string }> {
  const api = await playwrightRequest.newContext();
  try {
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };

    const quotes = await get<{ id: number; number: string; title: string; status: string }>(api, headers, '/quotes', {
      status: 'sent',
      limit: 100,
    });
    const existing = quotes.find((quote) => quote.title === QUOTE_TITLE && quote.status === 'sent');
    if (existing) {
      return { id: existing.id, number: existing.number };
    }

    const productIds: number[] = [];
    for (const product of [PRODUCT, SEAT_PRODUCT]) {
      const rows = await get<{ id: number; name: string }>(api, headers, '/products', { search: product.name, limit: 100 });
      const found = rows.find((row) => row.name === product.name);
      productIds.push(found ? found.id : (await post(api, headers, '/products', { ...product, currency: CURRENCY })).id);
    }

    const customers = await get<{ id: number; email: string }>(api, headers, '/customers', {
      search: CUSTOMER_EMAIL,
      limit: 100,
    });
    let customerId = customers.find((row) => row.email === CUSTOMER_EMAIL)?.id;
    if (!customerId) {
      customerId = (
        await post(api, headers, '/customers', {
          first_name: 'Mihai',
          last_name: 'Georgescu',
          email: CUSTOMER_EMAIL,
          company: 'Northwind Bakery',
          address: 'Strada Memorandumului 8',
          city: 'Cluj-Napoca',
          postal_code: '400114',
          country: 'Romania',
        })
      ).id;
    }

    const validUntil = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
    const quote = await post(api, headers, '/quotes', {
      title: QUOTE_TITLE,
      customer_id: customerId,
      currency: CURRENCY,
      valid_until: validUntil,
      notes: 'Support starts on the first day of the month after acceptance. Prices exclude VAT.',
    });
    await post(api, headers, `/quotes/${quote.id}/lines`, { product_id: productIds[0], quantity: 1, tax_rate_bp: 1900 });
    await post(api, headers, `/quotes/${quote.id}/lines`, {
      product_id: productIds[1],
      quantity: 12,
      discount_bp: 1000,
      tax_rate_bp: 1900,
    });
    await post(api, headers, `/quotes/${quote.id}/status`, { status: 'sent' });
    return { id: quote.id, number: quote.number };
  } finally {
    await api.dispose();
  }
}

test.describe('Screenshots - Quote response', () => {
  test('quote response page before an answer', async ({ page, browser }) => {
    test.setTimeout(180 * 1000);
    // The suite's sign-in, as every capture spec does; the client page itself
    // is opened in a separate context without a session.
    await ensureAdminLoggedIn(page);

    const quote = await ensureSentQuote();
    const linkPath = responseLinkPath(quote.id);

    const clientContext = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      deviceScaleFactor: 2,
    });
    try {
      const client = await clientContext.newPage();
      await client.goto(`${UI_BASE_URL}${linkPath}`);
      await expect(client.getByRole('heading', { name: `Quote ${quote.number}` })).toBeVisible(LOADED);
      // The page read the token once and dropped it from the address bar.
      await expect.poll(() => new URL(client.url()).search, LOADED).toBe('');
      await expect(client.getByRole('button', { name: 'Accept' })).toBeVisible();
      await expect(client.getByRole('button', { name: 'Decline' })).toBeVisible();

      // Captured before any answer: nothing is accepted or declined.
      await capture(client, 'quotes', '11-quote-response', { fullPage: true });
    } finally {
      await clientContext.close();
    }
  });
});
