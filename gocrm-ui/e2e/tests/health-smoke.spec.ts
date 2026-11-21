import { test, expect, type Response } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';

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

// `make e2e` (scripts/e2e/run.sh, which sets E2E_UI_PORT) builds the web app
// the way the release artifact does, with the relative API base /api/v1, and
// serves it with `vite preview`, which forwards /api/v1 to the backend. Only a
// manual run without the runner (the dev server) skips this test.
test('the web app under test is the production build', async ({ page, context }) => {
  test.skip(!process.env.E2E_UI_PORT, 'manual runs use the dev server');
  test.setTimeout(60_000);
  await context.clearCookies();
  await page.goto('/login');
  await page.evaluate(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  const requests: { url: URL; method: string; resourceType: string }[] = [];
  page.on('request', (request) => {
    requests.push({
      url: new URL(request.url()),
      method: request.method(),
      resourceType: request.resourceType(),
    });
  });
  const registration = page.waitForResponse(
    (response) =>
      response.request().method() === 'GET' &&
      new URL(response.url()).pathname === '/api/v1/auth/registration',
    { timeout: 30_000 },
  );
  await page.goto('/login');
  const registrationResponse = await registration;
  await expect(page.getByRole('heading', { name: 'Sign In', exact: true })).toBeVisible({
    timeout: 30_000,
  });

  const devPaths = requests
    .map((request) => request.url.pathname)
    .filter((pathname) => pathname.startsWith('/@vite/') || pathname.startsWith('/src/'));
  expect(devPaths, 'dev server module requests').toEqual([]);
  const bundleScripts = requests.filter(
    (request) =>
      request.resourceType === 'script' &&
      request.url.origin === UI_BASE_URL &&
      /^\/assets\/[^/]+\.js$/.test(request.url.pathname),
  );
  expect(bundleScripts.length, 'scripts loaded from /assets/*.js').toBeGreaterThan(0);
  expect(new URL(registrationResponse.url()).origin, 'API origin').toBe(UI_BASE_URL);
  expect(registrationResponse.status()).toBe(200);
});

// The header set `vite preview` sends under `make e2e`, the same values as the
// production nginx config (gocrm-ui/docker/nginx.conf, gocrm-ui/vite.config.ts).
const webAppHeaders: Record<string, string> = {
  'content-security-policy':
    "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
  'x-frame-options': 'DENY',
  'x-content-type-options': 'nosniff',
  'referrer-policy': 'same-origin',
};

test('the web app is served with its browser headers', async ({ page, context }) => {
  test.skip(!process.env.E2E_UI_PORT, 'manual runs use the dev server');
  test.setTimeout(120_000);
  await context.clearCookies();

  // Every content policy violation of every document in this tab is reported
  // here, across full navigations.
  const violations: { directive: string; blocked: string; page: string }[] = [];
  await page.exposeFunction(
    '__reportPolicyViolation',
    (violation: { directive: string; blocked: string; page: string }) => {
      violations.push(violation);
    },
  );
  await page.addInitScript(() => {
    const target = window as unknown as {
      __policyListener?: boolean;
      __reportPolicyViolation?: (v: { directive: string; blocked: string; page: string }) => void;
    };
    document.addEventListener(
      'securitypolicyviolation',
      (event) => {
        target.__reportPolicyViolation?.({
          directive: event.effectiveDirective,
          blocked: event.blockedURI,
          page: location.pathname,
        });
      },
      true,
    );
    target.__policyListener = true;
  });

  const expectHeaders = (response: Response | null, what: string) => {
    expect(response, `response of ${what}`).not.toBeNull();
    const headers = response?.headers() ?? {};
    for (const [name, value] of Object.entries(webAppHeaders)) {
      expect(headers[name], `${what} ${name}`).toBe(value);
    }
  };

  const asset = page.waitForResponse(
    (response) =>
      new URL(response.url()).origin === UI_BASE_URL &&
      /^\/assets\/[^/]+\.js$/.test(new URL(response.url()).pathname),
    { timeout: 30_000 },
  );
  expectHeaders(await page.goto('/'), '/');
  expectHeaders(await asset, 'asset script');
  expectHeaders(await page.goto('/auth/callback'), '/auth/callback');

  // Self-check: the listener is installed and does see a blocked request.
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: 'Sign In', exact: true })).toBeVisible({
    timeout: 30_000,
  });
  expect(
    await page.evaluate(
      () => (window as unknown as { __policyListener?: boolean }).__policyListener,
    ),
    'violation listener installed',
  ).toBe(true);
  await page.evaluate(() => {
    const probe = document.createElement('img');
    probe.src = 'https://example.invalid/probe.png';
    document.body.appendChild(probe);
  });
  await expect
    .poll(() => violations.map((v) => v.blocked).join(' '), { timeout: 15_000 })
    .toContain('example.invalid');
  violations.length = 0;

  await new AdminAuthHelper(page).ensureAdminLoggedIn();
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Dashboard', exact: true })).toBeVisible({
    timeout: 30_000,
  });
  await page.waitForLoadState('networkidle');
  await page.goto('/leads');
  await expect(page.getByRole('heading', { name: 'Leads', exact: true })).toBeVisible({
    timeout: 30_000,
  });
  await page.waitForLoadState('networkidle');
  expect(violations, 'content policy violations').toEqual([]);
});
