import { expect, type Page } from '@playwright/test';

/** Settings > Single sign-on (/settings/sso). */
export class SSOSettingsPage {
  constructor(readonly page: Page) {}

  async goto() {
    await this.page.goto('/settings/sso');
    await expect(this.page.getByRole('heading', { name: 'Single sign-on', level: 1 })).toBeVisible({
      timeout: 30 * 1000,
    });
  }

  get enabledSwitch() {
    return this.page.getByLabel('Enabled', { exact: true });
  }

  get issuerURL() {
    return this.page.getByLabel('Issuer URL', { exact: true });
  }

  get clientID() {
    return this.page.getByLabel('Client ID', { exact: true });
  }

  get clientSecret() {
    return this.page.getByLabel('Client secret', { exact: true });
  }

  get scopes() {
    return this.page.getByLabel('Scopes', { exact: true });
  }

  get secretStatus() {
    return this.page.getByTestId('sso-secret-status');
  }

  /** Adds a role-mapping row and fills it: claim, value and role (by name). */
  async addMapping(claim: 'groups' | 'roles', value: string, roleName: string) {
    const rows = this.page.locator('[data-testid^="sso-mapping-row-"]');
    const index = await rows.count();
    await this.page.getByRole('button', { name: 'Add mapping' }).click();
    const row = this.page.getByTestId(`sso-mapping-row-${index}`);
    await expect(row).toBeVisible();

    await row.getByRole('combobox', { name: 'Claim' }).click();
    await this.page.getByRole('option', { name: claim, exact: true }).click();
    await row.getByLabel('Value', { exact: true }).fill(value);
    await row.getByRole('combobox', { name: 'Role' }).click();
    await this.page.getByRole('option', { name: roleName, exact: true }).click();
    await expect(row.getByRole('combobox', { name: 'Role' })).toHaveText(roleName);
  }

  /** Presses Save and returns the PUT /settings/sso response. */
  async save() {
    const saved = this.page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith('/settings/sso') &&
        response.request().method() === 'PUT',
    );
    await this.page.getByRole('button', { name: 'Save', exact: true }).click();
    return saved;
  }
}
