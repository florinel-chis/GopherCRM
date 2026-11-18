import { execFileSync } from 'child_process';
import path from 'path';
import { fileURLToPath } from 'url';
import { test, expect } from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { expectedAmountText } from '../pages/deals.page';

/**
 * The client's side of a quote (issue #383): the link of the quote email
 * opens a public page, without a session, where the client accepts the quote
 * once. The staff page then shows the quote accepted and the link is dead.
 *
 * The link exists only in the mail body, which the log-only mailer of the e2e
 * backend never writes anywhere. `scripts/e2e/quote-response-link` emails the
 * quote through the real quote service with a capturing mailer and prints the
 * link; it runs only under `make e2e`. The link is a live credential: it is
 * never logged and never part of a test title or an assertion message.
 */

const repoRoot = path.resolve(fileURLToPath(new URL('.', import.meta.url)), '..', '..', '..');
const LINK_MARKER = '/quote-response?token=';
const INVALID_LINK = 'This link is no longer valid. It may already have been used or it has expired.';

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

test.describe('Quote response link', () => {
  test('a client accepts a sent quote through the emailed link, once, without signing in', async ({
    page,
    browser,
    request,
  }) => {
    test.setTimeout(120000);

    // 1. The quote, through the API: a customer with an address, a product,
    // one line (2 x 12.50 at 19 % tax: net 25.00, tax 4.75, gross 29.75), sent.
    const login = await request.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    const stamp = `${Date.now()}`;

    const customer = await request.post(`${API_BASE_URL}/customers`, {
      headers,
      data: { first_name: 'Quote', last_name: `Client ${stamp}`, email: `quote_client_${stamp}@example.com` },
    });
    expect(customer.status()).toBe(201);
    const customerId: number = (await customer.json()).data.id;

    const product = await request.post(`${API_BASE_URL}/products`, {
      headers,
      data: { name: `E2E Response Product ${stamp}`, sku: `QR-${stamp}`, unit_price_cents: 1250, currency: 'EUR' },
    });
    expect(product.status()).toBe(201);
    const productId: number = (await product.json()).data.id;

    const created = await request.post(`${API_BASE_URL}/quotes`, {
      headers,
      data: { title: `E2E Response Quote ${stamp}`, customer_id: customerId, currency: 'EUR' },
    });
    expect(created.status()).toBe(201);
    const quote = (await created.json()).data;
    const quoteId: number = quote.id;
    const quoteNumber: string = quote.number;
    expect(quoteNumber).toMatch(/^Q-\d{6}$/);

    const line = await request.post(`${API_BASE_URL}/quotes/${quoteId}/lines`, {
      headers,
      data: { product_id: productId, quantity: 2, tax_rate_bp: 1900 },
    });
    expect(line.status()).toBe(201);
    const sent = await request.post(`${API_BASE_URL}/quotes/${quoteId}/status`, {
      headers,
      data: { status: 'sent' },
    });
    expect(sent.status()).toBe(200);

    // 2. The link of the quote email.
    const linkPath = responseLinkPath(quoteId);

    // 3. The client: a fresh context with no session.
    const clientContext = await browser.newContext();
    try {
      const client = await clientContext.newPage();
      await client.goto(`${UI_BASE_URL}${linkPath}`);
      await expect(client.getByRole('heading', { name: `Quote ${quoteNumber}` })).toBeVisible();
      await expect(client).toHaveURL(/\/quote-response\?token=/);
      await expect(client.getByTestId('quote-response-line')).toHaveCount(1);
      await expect(client.getByTestId('quote-response-net')).toHaveText(expectedAmountText('25.00', 'EUR'));
      await expect(client.getByTestId('quote-response-tax')).toHaveText(expectedAmountText('4.75', 'EUR'));
      await expect(client.getByTestId('quote-response-gross')).toHaveText(expectedAmountText('29.75', 'EUR'));
      await expect(client.getByRole('button', { name: 'Decline' })).toBeVisible();
      // Reading the link sent no bearer token and did not spend it.
      expect(await client.evaluate(() => window.localStorage.length + window.sessionStorage.length)).toBe(0);

      await client.getByRole('button', { name: 'Accept' }).click();
      const dialog = client.getByRole('dialog', { name: `Accept quote ${quoteNumber}?` });
      await expect(dialog).toBeVisible();
      const responded = client.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.endsWith('/quotes/public/response') &&
          response.request().method() === 'POST'
      );
      await dialog.getByRole('button', { name: 'Confirm' }).click();
      const response = await responded;
      expect(response.status()).toBe(200);
      expect(response.request().headers()['authorization']).toBeUndefined();
      await expect(client.getByText(`You have accepted quote ${quoteNumber}.`)).toBeVisible();
      await expect(client.getByRole('button', { name: /accept|decline/i })).toHaveCount(0);

      // 4. Staff see the quote accepted.
      await new AdminAuthHelper(page).ensureAdminLoggedIn();
      await page.goto(`/quotes/${quoteId}`);
      await expect(page.getByTestId('quote-number')).toHaveText(quoteNumber);
      await expect(page.getByTestId('quote-status-chip')).toHaveText('Accepted');

      // 5. The link worked once: reopened, it shows no quote and no buttons.
      await client.goto(`${UI_BASE_URL}${linkPath}`);
      await expect(client.getByText(INVALID_LINK)).toBeVisible();
      await expect(client.getByText(quoteNumber)).toHaveCount(0);
      await expect(client.getByRole('button', { name: /accept|decline/i })).toHaveCount(0);
    } finally {
      await clientContext.close();
    }
  });
});
