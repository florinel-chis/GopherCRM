import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { API_BASE_URL } from '../helpers/env';

test('API health and unauthenticated access boundary', async ({ request }) => {
  const health = await request.get(new URL('/health', API_BASE_URL).toString());
  expect(health.status()).toBe(200);
  expect(await health.json()).toMatchObject({ success: true, data: { status: 'healthy' } });
  const privateData = await request.get(`${API_BASE_URL}/tickets`);
  expect(privateData.status()).toBe(401);
});

test('admin login and core entity pages load with successful API responses', async ({ page }) => {
  test.setTimeout(90_000);
  await new AdminAuthHelper(page).ensureAdminLoggedIn();
  for (const [path, heading] of [
    ['leads', 'Leads'],
    ['customers', 'Customers'],
    ['tickets', 'Tickets'],
    ['tasks', 'Tasks'],
    ['users', 'Users'],
  ]) {
    const response = page.waitForResponse(
      (value) =>
        value.request().method() === 'GET' &&
        new URL(value.url()).pathname === new URL(`${API_BASE_URL}/${path}`).pathname,
    );
    await page.goto(`/${path}`);
    expect((await response).status(), `${path} collection`).toBe(200);
    await expect(page.getByRole('heading', { name: heading, exact: true })).toBeVisible();
  }
});
