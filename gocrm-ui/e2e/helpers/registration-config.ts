import { request } from '@playwright/test';
import { API_BASE_URL } from './env';
import { testAdminCredentials } from '../fixtures/admin-user';

/**
 * Flips the security.allow_public_registration configuration through the
 * admin API. The backend seeds it "false" (sign-up is opt-in per deployment),
 * while most of this suite predates the switch and assumes an open
 * /auth/register — so global-setup turns it on once, and the disabled-state
 * tests in registration.spec.ts flip it off and back on around themselves.
 *
 * Creates its own APIRequestContext so it works from global setup and from
 * beforeAll/afterAll hooks alike (the `request` fixture is test-scoped).
 */
/**
 * Reads the current state of the switch through the public status endpoint,
 * so global setup can record what it found and teardown can put it back —
 * a screenshots run against the dev backend must not permanently reopen
 * sign-up in that deployment's database.
 */
export async function getPublicRegistration(): Promise<boolean> {
  const context = await request.newContext();
  try {
    const response = await context.get(`${API_BASE_URL}/auth/registration`);
    if (!response.ok()) {
      throw new Error(`reading the registration status failed (${response.status()}): ${await response.text()}`);
    }
    const { data } = await response.json();
    return data.enabled === true;
  } finally {
    await context.dispose();
  }
}

export async function setPublicRegistration(enabled: boolean): Promise<void> {
  const context = await request.newContext();
  try {
    const login = await context.post(`${API_BASE_URL}/auth/login`, {
      data: {
        email: testAdminCredentials.email,
        password: testAdminCredentials.password,
      },
    });
    if (!login.ok()) {
      throw new Error(`admin login failed (${login.status()}): ${await login.text()}`);
    }
    const { data } = await login.json();

    const response = await context.put(
      `${API_BASE_URL}/configurations/security.allow_public_registration`,
      {
        headers: { Authorization: `Bearer ${data.token}` },
        data: { value: enabled },
      }
    );
    if (!response.ok()) {
      throw new Error(
        `setting security.allow_public_registration=${enabled} failed (${response.status()}): ${await response.text()}`
      );
    }
  } finally {
    await context.dispose();
  }
}
