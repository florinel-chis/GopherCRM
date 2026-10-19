import { expect, test } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { API_BASE_URL } from '../helpers/env';
import { CustomersPage } from '../pages/customers.page';
import { generateCustomerData } from '../fixtures/admin-user';

test.beforeEach(async ({ page }) => {
  await new AdminAuthHelper(page).ensureAdminLoggedIn();
});

test('customer website survives creation and reload', async ({ page }) => {
  const customers = new CustomersPage(page);
  const data = { ...generateCustomerData(), website: 'https://example.com/original' };
  await page.goto('/customers/new');
  await customers.fillCustomerForm({
    companyName: data.companyName,
    contactName: data.contactName,
    email: data.email,
    phone: data.phone,
    website: data.website,
  });
  const created = await customers.saveAndWaitForResponse();
  expect(created.status()).toBe(201);
  const { data: customer } = await created.json();
  await page.waitForURL('**/customers');

  await page.goto(`/customers/${customer.id}`);
  await expect(page.getByRole('link', { name: data.website, exact: true })).toHaveAttribute(
    'href',
    data.website,
  );
  await page.goto(`/customers/${customer.id}/edit`);
  await expect(customers.websiteInput).toHaveValue(data.website);
});

for (const { action, website } of [
  { action: 'editing', website: 'http://example.com/updated' },
  { action: 'clearing', website: '' },
]) {
  test(`customer website survives ${action} and reload`, async ({ page }) => {
    const customers = new CustomersPage(page);
    const data = generateCustomerData();
    const originalWebsite = 'https://example.com/original';
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    const created = await page.request.post(`${API_BASE_URL}/customers`, {
      headers: { Authorization: `Bearer ${token}` },
      data: {
        first_name: 'Website',
        last_name: 'Persistence',
        email: data.email,
        company: data.companyName,
        phone: data.phone,
        website: originalWebsite,
      },
    });
    expect(created.status()).toBe(201);
    const { data: customer } = await created.json();
    await page.goto(`/customers/${customer.id}/edit`);
    await expect(customers.websiteInput).toHaveValue(originalWebsite);
    await customers.websiteInput.fill(website);
    const saved = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/customers/${customer.id}`) &&
        response.request().method() === 'PUT',
    );
    await customers.saveButton.click();
    expect((await saved).status()).toBe(200);
    await page.waitForURL('**/customers');
    await page.goto(`/customers/${customer.id}/edit`);
    await expect(customers.websiteInput).toHaveValue(website);
    await page.goto(`/customers/${customer.id}`);
    if (website) {
      await expect(page.getByRole('link', { name: website, exact: true })).toHaveAttribute(
        'href',
        website,
      );
    } else {
      // Wait for this customer's details before checking that the old link is gone.
      await expect(
        page.getByRole('heading', { name: data.companyName, exact: true }),
      ).toBeVisible();
      await expect(page.getByRole('link', { name: originalWebsite, exact: true })).toHaveCount(0);
    }
  });
}

test('lead conversion saves its website and opens the created customer', async ({ page }) => {
  const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
  const headers = { Authorization: `Bearer ${token}` };
  const me = await page.request.get(`${API_BASE_URL}/users/me`, { headers });
  expect(me.ok()).toBeTruthy();
  const { data: user } = await me.json();
  const customerData = generateCustomerData();
  const created = await page.request.post(`${API_BASE_URL}/leads`, {
    headers,
    data: {
      first_name: 'Website',
      last_name: 'Conversion',
      email: customerData.email,
      phone: customerData.phone,
      company: customerData.companyName,
      owner_id: user.id,
      status: 'qualified',
      source: 'website',
    },
  });
  expect(created.status()).toBe(201);
  const { data: lead } = await created.json();
  await page.goto(`/leads/${lead.id}`);
  await page.getByRole('button', { name: 'Convert to Customer' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Website').fill('  https://example.com/converted  ');
  const converted = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/leads/${lead.id}/convert`) &&
      response.request().method() === 'POST',
  );
  await dialog.getByRole('button', { name: 'Convert to Customer' }).click();
  const response = await converted;
  expect(response.status()).toBe(200);
  const { data: customer } = await response.json();
  await page.waitForURL(`**/customers/${customer.id}`);
  await page.reload();
  await expect(
    page.getByRole('link', { name: 'https://example.com/converted', exact: true }),
  ).toHaveAttribute('href', 'https://example.com/converted');
  await page.goto(`/customers/${customer.id}/edit`);
  await expect(page.getByLabel('Website')).toHaveValue('https://example.com/converted');
});
