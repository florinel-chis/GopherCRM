import { createHash, randomBytes } from 'node:crypto';
import {
  test,
  expect,
  request,
  type Browser,
  type BrowserContext,
  type Page,
  type Request,
} from '@playwright/test';
import { API_BASE_URL, UI_BASE_URL } from '../helpers/env';
import {
  APIClient,
  OIDC_CLIENT_ID,
  OIDC_CLIENT_SECRET,
  OIDC_ISSUER_URL,
  OIDC_REDIRECT_URI,
  SSO_FAILED_MESSAGE,
  SSO_OFF,
  passwordResetMailLines,
  sessionToken,
  ssoEnabledStatus,
  uniqueIdentity,
  type SSOSettingsBody,
} from '../helpers/sso';
import { SSOSettingsPage } from '../pages/sso-settings.page';
import { TestIssuerPage, type IssuerIdentity } from '../pages/test-issuer.page';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { testAdminCredentials } from '../fixtures/admin-user';

/**
 * OpenID Connect sign-in, end to end (#446): the real browser round trip
 * login page → test issuer page → /auth/callback → app, against the
 * disposable issuer `make e2e` starts (scripts/e2e/oidc-issuer). The issuer
 * serves the run's per-run test certificate, which the e2e backend build
 * trusts and the browser is told to accept here.
 *
 * Serial: the first test stores the settings every later test signs in
 * with. Every identity uses a unique subject and email; afterAll switches
 * single sign-on off again and deletes every user the spec created, so
 * login.spec.ts and the rest of the suite find it disabled.
 */
test.describe.configure({ mode: 'serial' });
test.use({ ignoreHTTPSErrors: true });

/** First assertion after a sign-in, a navigation or a data load: a loaded machine needs more than 5 s. */
const LOAD = 30 * 1000;
const CLAIM_ADMIN = 'crm-admin';
const CLAIM_SALES = 'crm-sales';
// Meets the password policy: 10+ characters, upper, lower, digit, special.
const LOCAL_PASSWORD = 'Local!Sales2026x';

const createdEmails = new Set<string>();

interface SignIn {
  context: BrowserContext;
  page: Page;
  /** Status of POST /auth/oidc/callback. */
  status: number;
}

const isCallbackPage = (url: URL) =>
  url.origin === UI_BASE_URL && url.pathname === '/auth/callback';

/** Signs in through the login page and the issuer, in a browser context of its own. */
async function ssoSignIn(browser: Browser, identity: IssuerIdentity): Promise<SignIn> {
  if (identity.email) createdEmails.add(identity.email.toLowerCase());
  const context = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: UI_BASE_URL });
  const page = await context.newPage();
  await page.goto('/login');
  await page.getByRole('button', { name: 'Sign in with single sign-on' }).click();
  await page.waitForURL((url) => url.href.startsWith(`${OIDC_ISSUER_URL}/authorize?`));
  const callback = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/auth/oidc/callback') &&
      response.request().method() === 'POST',
  );
  await new TestIssuerPage(page).signIn(identity);
  return { context, page, status: (await callback).status() };
}

/** A sign-in that must succeed: the app opens on / with a stored session. */
async function ssoSignInOK(
  browser: Browser,
  identity: IssuerIdentity,
): Promise<SignIn & { token: string }> {
  const signIn = await ssoSignIn(browser, identity);
  expect(signIn.status, 'POST /auth/oidc/callback').toBe(200);
  await signIn.page.waitForURL(`${UI_BASE_URL}/`);
  const token = await sessionToken(signIn.page);
  expect(token).toBeTruthy();
  return { ...signIn, token: token as string };
}

/** The generic refusal on the callback page, with the way back to /login. */
async function expectRefusal(page: Page) {
  await expect(page.getByText(SSO_FAILED_MESSAGE)).toBeVisible({ timeout: LOAD });
  const back = page.getByRole('link', { name: 'Back to sign in' });
  await expect(back).toBeVisible();
  await expect(back).toHaveAttribute('href', '/login');
  expect(await sessionToken(page)).toBeNull();
}

async function ssoSignInRefused(browser: Browser, identity: IssuerIdentity) {
  const signIn = await ssoSignIn(browser, identity);
  try {
    expect(signIn.status, 'POST /auth/oidc/callback').toBe(401);
    expect(isCallbackPage(new URL(signIn.page.url()))).toBeTruthy();
    await expectRefusal(signIn.page);
  } finally {
    await signIn.context.close();
  }
}

async function userIdOf(token: string): Promise<number> {
  const client = await APIClient.withToken(token);
  try {
    const response = await client.get('/users/me');
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data.id;
  } finally {
    await client.dispose();
  }
}

/** The settings S1 stores, as the API returns them, for a full-replacement PUT. */
async function currentSettings(admin: APIClient): Promise<SSOSettingsBody> {
  const stored = await admin.getSSO();
  return {
    enabled: stored.enabled,
    allow_password: stored.allow_password,
    issuer_url: stored.issuer_url,
    client_id: stored.client_id,
    scopes: stored.scopes,
    role_mapping: stored.role_mapping.map(({ claim, value, role_id }) => ({
      claim,
      value,
      role_id,
    })),
  };
}

/** Opens a callback URL and returns the status of the POST it triggers. */
async function openCallback(page: Page, url: string): Promise<number> {
  const callback = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/auth/oidc/callback') &&
      response.request().method() === 'POST',
  );
  await page.goto(url);
  return (await callback).status();
}

const isCallbackAPI = (url: URL) => url.pathname.endsWith('/auth/oidc/callback');

/**
 * Captures the callback URL of the next sign-in without spending its code or
 * state. The issuer reaches the callback page through a 303 redirect, and
 * Playwright does not route a navigation that a redirect produced, so the URL
 * is read from the request event and the SPA's POST /auth/oidc/callback is
 * aborted instead: the backend never sees the code or the state, and the page
 * shows the generic refusal.
 */
async function captureCallback(page: Page, signIn: () => Promise<void>): Promise<string> {
  let captured = '';
  const onRequest = (req: Request) => {
    if (req.isNavigationRequest() && isCallbackPage(new URL(req.url()))) captured = req.url();
  };
  page.on('request', onRequest);
  await page.route(isCallbackAPI, (route) => route.abort());
  try {
    await signIn();
    await expect.poll(() => captured, { timeout: LOAD }).not.toBe('');
    await page.waitForURL(isCallbackPage);
    await expect(page.getByText(SSO_FAILED_MESSAGE)).toBeVisible({ timeout: LOAD });
  } finally {
    await page.unroute(isCallbackAPI);
    page.off('request', onRequest);
  }
  return captured;
}

test.describe('Single sign-on with the test issuer', () => {
  // Several browser round trips per test.
  test.beforeEach(() => {
    test.setTimeout(120 * 1000);
  });
  test.skip(
    !OIDC_ISSUER_URL,
    'needs the OpenID Connect test issuer that make e2e starts (E2E_OIDC_ISSUER_URL)',
  );

  test.beforeAll(async () => {
    // A clean start, also after an aborted earlier run against the same database.
    const admin = await APIClient.admin();
    try {
      await admin.putSSO(SSO_OFF);
    } finally {
      await admin.dispose();
    }
  });

  test.afterAll(async () => {
    const admin = await APIClient.admin();
    try {
      await admin.putSSO(SSO_OFF);
      for (const email of createdEmails) {
        for (const user of await admin.usersByEmail(email)) {
          await admin.deleteUser(user.id);
        }
      }
    } finally {
      await admin.dispose();
    }
    expect(await ssoEnabledStatus()).toBe(false);
  });

  test('S1: admin saves the single sign-on settings through the settings screen', async ({
    page,
    browser,
  }) => {
    const admin = await APIClient.admin();
    const adminRole = await admin.roleId('admin');
    const salesRole = await admin.roleId('sales');

    await new AdminAuthHelper(page).ensureAdminLoggedIn();
    const settings = new SSOSettingsPage(page);
    await settings.goto();
    await settings.enabledSwitch.check();
    await settings.issuerURL.fill(OIDC_ISSUER_URL);
    await settings.clientID.fill(OIDC_CLIENT_ID);
    await settings.clientSecret.fill(OIDC_CLIENT_SECRET);
    await settings.scopes.fill('openid profile email');
    await settings.addMapping('roles', CLAIM_ADMIN, adminRole.name);
    await settings.addMapping('roles', CLAIM_SALES, salesRole.name);
    const saved = await settings.save();
    expect(saved.status(), await saved.text()).toBe(200);

    await page.reload();
    await expect(settings.secretStatus).toHaveText('A client secret is set', { timeout: LOAD });
    await expect(settings.enabledSwitch).toBeChecked();
    await expect(settings.issuerURL).toHaveValue(OIDC_ISSUER_URL);
    await expect(settings.clientSecret).toHaveValue('');
    expect(await page.content()).not.toContain(OIDC_CLIENT_SECRET);

    const stored = await admin.getSSO();
    expect(stored).toMatchObject({
      enabled: true,
      allow_password: false,
      issuer_url: OIDC_ISSUER_URL,
      client_id: OIDC_CLIENT_ID,
      scopes: ['openid', 'profile', 'email'],
      client_secret_set: true,
      role_mapping: [
        { claim: 'roles', value: CLAIM_ADMIN, role_id: adminRole.id },
        { claim: 'roles', value: CLAIM_SALES, role_id: salesRole.id },
      ],
    });
    expect(JSON.stringify(stored)).not.toContain(OIDC_CLIENT_SECRET);
    expect(await ssoEnabledStatus()).toBe(true);

    // A sales user signed in by password has no way in, in the UI or the API.
    const salesEmail = uniqueIdentity('settings-sales').email;
    createdEmails.add(salesEmail);
    await admin.createUser(salesEmail, LOCAL_PASSWORD, 'sales');
    await admin.dispose();

    const salesContext = await browser.newContext({ baseURL: UI_BASE_URL });
    try {
      const salesPage = await salesContext.newPage();
      await salesPage.goto('/login');
      await salesPage.locator('input[name="email"]').fill(salesEmail);
      await salesPage.locator('input[name="password"]').fill(LOCAL_PASSWORD);
      await salesPage.getByRole('button', { name: 'Sign In', exact: true }).click();
      await salesPage.waitForURL(`${UI_BASE_URL}/`);
      await expect(
        salesPage.getByTestId('app-navigation').getByText('Leads', { exact: true }),
      ).toBeVisible({ timeout: LOAD });
      await salesPage.goto('/settings/sso');
      await salesPage.waitForURL(`${UI_BASE_URL}/unauthorized`);
    } finally {
      await salesContext.close();
    }
    const sales = await APIClient.login(salesEmail, LOCAL_PASSWORD);
    try {
      expect((await sales.get('/settings/sso')).status()).toBe(403);
    } finally {
      await sales.dispose();
    }
  });

  test('S2: first sign-in with a mapped admin role provisions one user', async ({ browser }) => {
    const who = uniqueIdentity('admin');
    const first = await ssoSignInOK(browser, { ...who, roles: [CLAIM_ADMIN] });
    let firstId: number;
    try {
      const navigation = first.page.getByTestId('app-navigation');
      await expect(navigation.getByText('Users', { exact: true })).toBeVisible({ timeout: LOAD });
      firstId = await userIdOf(first.token);

      await first.page.goto('/users');
      await first.page.getByPlaceholder('Search users...').fill(who.email);
      await expect(first.page.getByText(who.email, { exact: true })).toBeVisible({
        timeout: LOAD,
      });
    } finally {
      await first.context.close();
    }

    const second = await ssoSignInOK(browser, { ...who, roles: [CLAIM_ADMIN] });
    try {
      expect(await userIdOf(second.token)).toBe(firstId);
    } finally {
      await second.context.close();
    }
    const admin = await APIClient.admin();
    try {
      const users = await admin.usersByEmail(who.email);
      expect(users).toHaveLength(1);
      expect(users[0]).toMatchObject({ id: firstId, role: 'admin', is_active: true });
    } finally {
      await admin.dispose();
    }
  });

  test('S3: sales role shows the sales navigation and no admin screens', async ({ browser }) => {
    const signIn = await ssoSignInOK(browser, { ...uniqueIdentity('sales'), roles: [CLAIM_SALES] });
    try {
      const navigation = signIn.page.getByTestId('app-navigation');
      await expect(navigation.getByText('Leads', { exact: true })).toBeVisible({ timeout: LOAD });
      await expect(navigation.getByText('Users', { exact: true })).toHaveCount(0);
      await expect(navigation.getByText('Single sign-on', { exact: true })).toHaveCount(0);
      await signIn.page.goto('/users');
      await signIn.page.waitForURL(`${UI_BASE_URL}/unauthorized`);
    } finally {
      await signIn.context.close();
    }
  });

  test('S4: no matching claim is refused and nobody is provisioned', async ({ browser }) => {
    const who = uniqueIdentity('unmapped');
    await ssoSignInRefused(browser, { ...who, roles: ['crm-unknown'] });
    const admin = await APIClient.admin();
    try {
      expect(await admin.usersByEmail(who.email)).toHaveLength(0);
    } finally {
      await admin.dispose();
    }
  });

  test('S5: CRM role follows the issuer on the next sign-in', async ({ browser }) => {
    const who = uniqueIdentity('follows');
    const asAdmin = await ssoSignInOK(browser, { ...who, roles: [CLAIM_ADMIN] });
    await asAdmin.context.close();
    const asSales = await ssoSignInOK(browser, { ...who, roles: [CLAIM_SALES] });
    await asSales.context.close();

    const admin = await APIClient.admin();
    try {
      const users = await admin.usersByEmail(who.email);
      expect(users).toHaveLength(1);
      expect(users[0].role).toBe('sales');
    } finally {
      await admin.dispose();
    }
  });

  test('S6: the only active admin cannot be demoted at the issuer', async ({ browser }) => {
    const who = uniqueIdentity('last-admin');
    const signIn = await ssoSignInOK(browser, { ...who, roles: [CLAIM_ADMIN] });
    await signIn.context.close();
    // Every write here uses the SSO admin's own token: deactivating the seeded
    // admin may end the seeded admin's sessions.
    const ssoAdmin = await APIClient.withToken(signIn.token);
    const ssoAdminId = await userIdOf(signIn.token);
    const deactivated: number[] = [];
    try {
      for (const other of await ssoAdmin.activeAdmins()) {
        if (other.id === ssoAdminId) continue;
        await ssoAdmin.setActive(other.id, false);
        deactivated.push(other.id);
      }
      expect((await ssoAdmin.activeAdmins()).map((row) => row.id)).toEqual([ssoAdminId]);

      await ssoSignInRefused(browser, { ...who, roles: [CLAIM_SALES] });

      expect(await ssoAdmin.userById(ssoAdminId)).toMatchObject({ role: 'admin', is_active: true });
      expect((await ssoAdmin.activeAdmins()).length).toBeGreaterThanOrEqual(1);
    } finally {
      for (const id of deactivated) {
        await ssoAdmin.setActive(id, true);
      }
      await ssoAdmin.dispose();
    }
    // The seeded admin signs in again for the rest of the spec and teardown.
    const admin = await APIClient.admin();
    await admin.dispose();
  });

  test('S7: email of an existing local user is refused in any letter case', async ({ browser }) => {
    const local = uniqueIdentity('local');
    createdEmails.add(local.email);
    const admin = await APIClient.admin();
    try {
      const created = await admin.createUser(local.email, LOCAL_PASSWORD, 'sales');

      await ssoSignInRefused(browser, {
        subject: local.subject,
        email: local.email.toUpperCase(),
        roles: [CLAIM_ADMIN],
      });

      const users = await admin.usersByEmail(local.email);
      expect(users).toHaveLength(1);
      expect(users[0]).toMatchObject({ id: created.id, role: 'sales', is_active: true });
      const byPassword = await APIClient.login(local.email, LOCAL_PASSWORD);
      await byPassword.dispose();
    } finally {
      await admin.dispose();
    }
  });

  test('S8: deactivated SSO user is refused', async ({ browser }) => {
    const who = uniqueIdentity('deactivated');
    const signIn = await ssoSignInOK(browser, { ...who, roles: [CLAIM_SALES] });
    const id = await userIdOf(signIn.token);
    await signIn.context.close();

    const admin = await APIClient.admin();
    try {
      await admin.setActive(id, false);
    } finally {
      await admin.dispose();
    }
    await ssoSignInRefused(browser, { ...who, roles: [CLAIM_SALES] });
  });

  test('S9: callback is single use and state is checked', async ({ browser }) => {
    const who = uniqueIdentity('callback');

    // A normal sign-in, with the callback URL the issuer redirected to.
    const context = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: UI_BASE_URL });
    createdEmails.add(who.email);
    let callbackURL = '';
    try {
      const page = await context.newPage();
      page.on('request', (req) => {
        if (req.isNavigationRequest() && isCallbackPage(new URL(req.url())))
          callbackURL = req.url();
      });
      await page.goto('/login');
      await page.getByRole('button', { name: 'Sign in with single sign-on' }).click();
      await page.waitForURL((url) => url.href.startsWith(`${OIDC_ISSUER_URL}/authorize?`));
      await new TestIssuerPage(page).signIn({ ...who, roles: [CLAIM_SALES] });
      await page.waitForURL(`${UI_BASE_URL}/`);
      const landed = new URL(page.url());
      expect(landed.searchParams.has('code')).toBe(false);
      expect(landed.searchParams.has('state')).toBe(false);
      expect(new URL(callbackURL).searchParams.get('code')).toBeTruthy();
    } finally {
      await context.close();
    }

    // The same callback URL in a fresh context: spent, refused, no session.
    const replay = await browser.newContext({ baseURL: UI_BASE_URL });
    try {
      const page = await replay.newPage();
      expect(await openCallback(page, callbackURL)).toBe(401);
      await expectRefusal(page);
      await expect(page).not.toHaveURL(`${UI_BASE_URL}/`);
    } finally {
      await replay.close();
    }

    // A captured, unspent callback URL with one character of state changed is
    // refused; the untouched URL then still signs in, so the tamper was the
    // reason for the refusal.
    const tamper = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: UI_BASE_URL });
    try {
      const page = await tamper.newPage();
      const captured = await captureCallback(page, async () => {
        await page.goto('/login');
        await page.getByRole('button', { name: 'Sign in with single sign-on' }).click();
        await page.waitForURL((url) => url.href.startsWith(`${OIDC_ISSUER_URL}/authorize?`));
        await new TestIssuerPage(page).signIn({ ...who, roles: [CLAIM_SALES] });
      });
      const url = new URL(captured);
      const state = url.searchParams.get('state') ?? '';
      expect(state).not.toBe('');
      const last = state.slice(-1);
      url.searchParams.set('state', state.slice(0, -1) + (last === 'A' ? 'B' : 'A'));

      expect(await openCallback(page, url.toString())).toBe(401);
      await expectRefusal(page);

      expect(await openCallback(page, captured)).toBe(200);
      await page.waitForURL(`${UI_BASE_URL}/`);
      expect(await sessionToken(page)).toBeTruthy();
    } finally {
      await tamper.close();
    }
  });

  test('S10: break-glass password login while single sign-on is on', async ({ browser }) => {
    expect(await ssoEnabledStatus()).toBe(true);
    const context = await browser.newContext({ baseURL: UI_BASE_URL });
    try {
      const page = await context.newPage();
      await page.goto('/login');
      await expect(page.getByRole('button', { name: 'Sign in with single sign-on' })).toBeVisible({
        timeout: LOAD,
      });
      await page.locator('input[name="email"]').fill(testAdminCredentials.email);
      await page.locator('input[name="password"]').fill(testAdminCredentials.password);
      await page.getByRole('button', { name: 'Sign In', exact: true }).click();
      await page.waitForURL(`${UI_BASE_URL}/`);
      await expect(
        page.getByTestId('app-navigation').getByText('Users', { exact: true }),
      ).toBeVisible({ timeout: LOAD });
    } finally {
      await context.close();
    }
  });

  // The password-login half of allow_password for a linked account stays with
  // the backend tests (TestSSOAccountPasswordPolicySQLite,
  // TestSSOAccountPasswordPolicyMySQL, TestSSOPasswordPolicyWired): a
  // provisioned account has no known password, and the log mailer redacts the
  // reset token, so no browser can set one.
  test('S11: allow_password governs password reset for SSO accounts', async ({ browser }) => {
    const who = uniqueIdentity('reset');
    const signIn = await ssoSignInOK(browser, { ...who, roles: [CLAIM_SALES] });
    await signIn.context.close();

    const anonymous = await request.newContext();
    const admin = await APIClient.admin();
    const settings = await currentSettings(admin);
    expect(settings.allow_password).toBe(false);
    try {
      const before = passwordResetMailLines(who.email);
      const off = await anonymous.post(`${API_BASE_URL}/auth/password-reset`, {
        data: { email: who.email },
      });
      expect(off.status()).toBe(200);
      // The mail is logged before the 200, so nothing more can arrive.
      expect(passwordResetMailLines(who.email)).toBe(before);

      await admin.putSSO({ ...settings, allow_password: true });
      const on = await anonymous.post(`${API_BASE_URL}/auth/password-reset`, {
        data: { email: who.email },
      });
      expect(on.status()).toBe(200);
      await expect
        .poll(() => passwordResetMailLines(who.email), { timeout: LOAD })
        .toBe(before + 1);
    } finally {
      await admin.putSSO({ ...settings, allow_password: false });
      await admin.dispose();
      await anonymous.dispose();
    }
  });

  test('S12: test issuer enforces redirect_uri, PKCE and nonce', async ({ page }) => {
    const verifier = randomBytes(32).toString('base64url');
    const challenge = createHash('sha256').update(verifier).digest('base64url');
    const state = randomBytes(16).toString('base64url');
    const nonce = randomBytes(16).toString('base64url');
    const authorizeURL = (overrides: Record<string, string | null>) => {
      const url = new URL(`${OIDC_ISSUER_URL}/authorize`);
      const params: Record<string, string | null> = {
        response_type: 'code',
        client_id: OIDC_CLIENT_ID,
        redirect_uri: OIDC_REDIRECT_URI,
        scope: 'openid profile email',
        state,
        nonce,
        code_challenge: challenge,
        code_challenge_method: 'S256',
        ...overrides,
      };
      for (const [name, value] of Object.entries(params)) {
        if (value !== null) url.searchParams.set(name, value);
      }
      return url.toString();
    };

    // Refused requests stay on the issuer and never redirect.
    const refused: Record<string, string | null>[] = [
      { redirect_uri: `${UI_BASE_URL}/somewhere-else` },
      { nonce: null },
    ];
    for (const overrides of refused) {
      const response = await page.goto(authorizeURL(overrides));
      expect(response?.status()).toBe(400);
      await expect(page.getByText('Authorization request refused')).toBeVisible({ timeout: LOAD });
      expect(page.url().startsWith(`${OIDC_ISSUER_URL}/authorize?`)).toBe(true);
    }

    // A valid request: sign in and capture the code without the SPA spending it.
    const captured = await captureCallback(page, async () => {
      await page.goto(authorizeURL({}));
      await new TestIssuerPage(page).signIn({ ...uniqueIdentity('client'), roles: [CLAIM_SALES] });
    });
    const callback = new URL(captured);
    expect(callback.searchParams.get('state')).toBe(state);
    const code = callback.searchParams.get('code') ?? '';
    expect(code).not.toBe('');

    const issuer = await request.newContext({ ignoreHTTPSErrors: true });
    const basic = `Basic ${Buffer.from(
      `${encodeURIComponent(OIDC_CLIENT_ID)}:${encodeURIComponent(OIDC_CLIENT_SECRET)}`,
    ).toString('base64')}`;
    const exchange = (redirectURI: string, codeVerifier: string) =>
      issuer.post(`${OIDC_ISSUER_URL}/token`, {
        headers: { Authorization: basic },
        form: {
          grant_type: 'authorization_code',
          code,
          redirect_uri: redirectURI,
          code_verifier: codeVerifier,
        },
      });
    try {
      const wrongRedirect = await exchange(`${UI_BASE_URL}/somewhere-else`, verifier);
      expect(wrongRedirect.status()).toBe(400);
      expect((await wrongRedirect.json()).error).toBe('invalid_grant');

      const wrongVerifier = await exchange(
        OIDC_REDIRECT_URI,
        randomBytes(32).toString('base64url'),
      );
      expect(wrongVerifier.status()).toBe(400);
      expect((await wrongVerifier.json()).error).toBe('invalid_grant');

      const ok = await exchange(OIDC_REDIRECT_URI, verifier);
      expect(ok.status(), await ok.text()).toBe(200);
      const idToken: string = (await ok.json()).id_token;
      const payload = JSON.parse(Buffer.from(idToken.split('.')[1], 'base64url').toString('utf8'));
      expect(payload.nonce).toBe(nonce);
      expect(payload.aud).toBe(OIDC_CLIENT_ID);
      expect(payload.iss).toBe(OIDC_ISSUER_URL);

      const reused = await exchange(OIDC_REDIRECT_URI, verifier);
      expect(reused.status()).toBe(400);
      expect((await reused.json()).error).toBe('invalid_grant');
    } finally {
      await issuer.dispose();
    }
  });

  test('S13: a sign-in finishes only in the browser that started it', async ({ browser }) => {
    const who = uniqueIdentity('binding');
    createdEmails.add(who.email);
    const cookieName = 'gophercrm_oidc_state';
    const cookiePath = `${new URL(API_BASE_URL).pathname}/auth/oidc`;

    // The audit totals of the two refusal reasons, before this test.
    const admin = await APIClient.admin();
    const auditTotal = async (reason: string): Promise<number> => {
      const response = await admin.get('/audit/sign-ins', { method: 'oidc', reason });
      expect(response.ok(), await response.text()).toBeTruthy();
      // meta.total is omitted when it is 0.
      return (await response.json()).meta?.total ?? 0;
    };
    const stateOf = (callbackURL: string) => new URL(callbackURL).searchParams.get('state') ?? '';
    const signInCookie = async (context: BrowserContext) =>
      (await context.cookies()).find((cookie) => cookie.name === cookieName);
    const startAndCapture = (page: Page) =>
      captureCallback(page, async () => {
        await page.goto('/login');
        await page.getByRole('button', { name: 'Sign in with single sign-on' }).click();
        await page.waitForURL((url) => url.href.startsWith(`${OIDC_ISSUER_URL}/authorize?`));
        await new TestIssuerPage(page).signIn({ ...who, roles: [CLAIM_SALES] });
      });

    const contexts: BrowserContext[] = [];
    const newContext = async () => {
      const context = await browser.newContext({ ignoreHTTPSErrors: true, baseURL: UI_BASE_URL });
      contexts.push(context);
      return context;
    };
    try {
      const missingBefore = await auditTotal('binding_missing');
      const mismatchBefore = await auditTotal('binding_mismatch');

      // Browser A starts a sign-in; its callback URL U1 is captured unspent.
      const a = await newContext();
      const pageA = await a.newPage();
      const u1 = await startAndCapture(pageA);
      expect(stateOf(u1)).not.toBe('');
      const cookie = await signInCookie(a);
      expect(cookie, 'the sign-in cookie of browser A').toBeTruthy();
      expect(cookie?.httpOnly).toBe(true);
      expect(cookie?.sameSite).toBe('Lax');
      expect(cookie?.path).toBe(cookiePath);
      expect(cookie?.value).toBe(stateOf(u1));

      // U1 opened in a fresh browser B, which holds no cookie: refused.
      const b = await newContext();
      const pageB = await b.newPage();
      expect(await openCallback(pageB, u1)).toBe(401);
      await expectRefusal(pageB);

      // A second sign-in in A replaces A's cookie: only the latest start in a
      // browser can finish.
      const u2 = await startAndCapture(pageA);
      expect(stateOf(u2)).not.toBe(stateOf(u1));
      expect((await signInCookie(a))?.value).toBe(stateOf(u2));

      // Browser C starts a sign-in of its own, stops at the issuer, then opens
      // U2: its cookie names another sign-in, so it is refused.
      const c = await newContext();
      const pageC = await c.newPage();
      await pageC.goto('/login');
      await pageC.getByRole('button', { name: 'Sign in with single sign-on' }).click();
      await pageC.waitForURL((url) => url.href.startsWith(`${OIDC_ISSUER_URL}/authorize?`), {
        timeout: LOAD,
      });
      const cookieC = await signInCookie(c);
      expect(cookieC?.value).toBeTruthy();
      expect(cookieC?.value).not.toBe(stateOf(u2));
      expect(await openCallback(pageC, u2)).toBe(401);
      await expectRefusal(pageC);

      expect(await auditTotal('binding_missing')).toBe(missingBefore + 1);
      expect(await auditTotal('binding_mismatch')).toBe(mismatchBefore + 1);
    } finally {
      for (const context of contexts) await context.close();
      await admin.dispose();
    }
  });
});
