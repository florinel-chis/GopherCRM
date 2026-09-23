import { test, expect, type APIRequestContext } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LeadsPage } from '../pages/leads.page';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';


// The search assertions below look for this exact lead, so the suite provisions
// it itself instead of relying on rows that happen to sit in a developer's database.
const SEARCH_LEAD = {
  first_name: 'Anders',
  last_name: 'Thomsen',
  email: 'anders.t@conversio.dk',
  phone: '+45 31 22 44 88',
  company: 'Conversio',
  source: 'website',
  status: 'new',
};

// A second lead that matches none of the search terms used below, so the
// unfiltered list always holds rows a search can filter out.
const CONTROL_LEAD = {
  first_name: 'Oskar',
  last_name: 'Berg',
  email: 'oskar.b@northwind-trading.example',
  phone: '+46 8 555 010 20',
  company: 'Northwind Trading',
  source: 'referral',
  status: 'new',
};

type ProvisionedLead = typeof SEARCH_LEAD;

/**
 * Creates the leads these tests search for, if they are not already there.
 * Safe to re-run.
 */
async function provisionSearchLeads(request: APIRequestContext): Promise<void> {
  const loginResponse = await request.post(`${API_BASE_URL}/auth/login`, {
    data: {
      email: testAdminCredentials.email,
      password: testAdminCredentials.password,
    },
  });
  expect(loginResponse.status(), 'admin login for lead provisioning').toBe(200);

  const loginBody = await loginResponse.json();
  const token: string = loginBody?.data?.token;
  expect(token, 'admin token for lead provisioning').toBeTruthy();
  const headers = { Authorization: `Bearer ${token}` };

  // Admin-created leads need an explicit owner_id; the API 400s without one.
  let ownerId: number | undefined = loginBody?.data?.user?.id;
  const users = await request.get(`${API_BASE_URL}/users`, {
    headers,
    params: { search: testAdminCredentials.email, limit: 5 },
  });
  if (users.ok()) {
    const body = await users.json();
    const match = (body?.data ?? []).find(
      (user: { id?: number; email?: string }) => user.email === testAdminCredentials.email
    );
    if (match?.id) {
      ownerId = match.id;
    }
  }
  expect(ownerId, 'owner id for the provisioned leads').toBeTruthy();

  for (const lead of [SEARCH_LEAD, CONTROL_LEAD] as ProvisionedLead[]) {
    const existing = await request.get(`${API_BASE_URL}/leads`, {
      headers,
      params: { search: lead.email, limit: 5 },
    });
    if (existing.ok()) {
      const body = await existing.json();
      const leads: Array<{ email?: string }> = body?.data?.leads ?? [];
      if (leads.some(existingLead => existingLead.email === lead.email)) {
        continue;
      }
    }

    const created = await request.post(`${API_BASE_URL}/leads`, {
      headers,
      data: { ...lead, owner_id: ownerId },
    });

    // 409 means a concurrent run got there first — equally fine.
    if (!created.ok() && created.status() !== 409) {
      throw new Error(
        `Failed to provision lead ${lead.email}: ${created.status()} ${await created.text()}`
      );
    }
  }
}

test.describe('Leads List - Sorting and Search', () => {
  let adminAuth: AdminAuthHelper;

  test.beforeAll(async ({ request }) => {
    await provisionSearchLeads(request);
  });

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('should load leads page with data', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.pageTitle).toBeVisible();
    await expect(leadsPage.leadsTable).toBeVisible();

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should sort by Created column descending', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();
    await expect(createdHeader).toBeVisible();

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    const requestUrl = response.request().url();
    expect(requestUrl).toContain('sort_by=created_at');
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should toggle sort order on double click', async ({ page }) => {
    // Regression guard: DataTable used to keep `order`/`orderBy` in its own
    // state, which the list page threw away on every refetch (a sort click
    // changes the query key, the page renders its loading branch and the table
    // unmounts). The header fell back to inactive and the next click recomputed
    // ascending, making `desc` unreachable. The sorted column is owned by the
    // page now and handed to DataTable as controlled `sortBy`/`sortOrder`.
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const createdHeaderCell = page.locator('th').filter({ hasText: 'Created' }).first();
    const createdHeader = createdHeaderCell.locator('span[role="button"]').first();

    // Asserted through the rendered table rather than the network: the second
    // sort key may already sit in the TanStack Query cache, in which case the app
    // legitimately issues no request.
    await createdHeader.click();
    await expect(createdHeaderCell).toHaveAttribute('aria-sort', 'ascending');
    const ascendingFirstRow = await leadsPage.tableRows.first().innerText();

    await createdHeader.click();
    await expect(createdHeaderCell).toHaveAttribute('aria-sort', 'descending');
    await expect(leadsPage.tableRows.first()).not.toHaveText(ascendingFirstRow);
  });

  test('should search for a lead by email', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );

    await leadsPage.searchInput.fill('anders.t@conversio.dk');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThanOrEqual(1);

    const tableText = await leadsPage.leadsTable.textContent();
    expect(tableText).toContain('anders.t@conversio.dk');
  });

  test('should search for a lead by company name', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );

    await leadsPage.searchInput.fill('Conversio');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThanOrEqual(1);

    const tableText = await leadsPage.leadsTable.textContent();
    expect(tableText).toContain('Conversio');
  });

  test('should search and sort together', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('conversio');
    await responsePromise;

    await page.waitForLoadState('networkidle');

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();

    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    const url = response.request().url();
    expect(url).toContain('search=conversio');
    expect(url).toContain('sort_by=created_at');
    expect(response.status()).toBe(200);
  });

  test('should clear search and show all leads', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    // Two leads are provisioned in beforeAll, so the unfiltered list always has
    // rows the search below filters out.
    const initialCount = await leadsPage.tableRows.count();
    expect(initialCount).toBeGreaterThan(1);

    // Clearing the search returns to a query key TanStack Query already holds,
    // so the restored list can render without any network traffic. Assert the
    // rendered rows instead of waiting for a response that may never come.
    await leadsPage.searchInput.fill(SEARCH_LEAD.email);
    await expect(leadsPage.tableRows).toHaveCount(1);
    await expect(leadsPage.leadsTable).toContainText(SEARCH_LEAD.email);
    await expect(leadsPage.leadsTable).not.toContainText(CONTROL_LEAD.email);

    await leadsPage.searchInput.clear();
    await expect(leadsPage.tableRows).toHaveCount(initialCount);
  });

  test('should return no results for non-existent search', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('xyznonexistent12345abcdef');
    const response = await responsePromise;

    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBe(0);
  });
});
