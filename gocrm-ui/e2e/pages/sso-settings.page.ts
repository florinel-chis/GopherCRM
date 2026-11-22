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

  get clientCertificate() {
    return this.page.getByLabel('Client certificate (PEM)', { exact: true });
  }

  get clientPrivateKey() {
    return this.page.getByLabel('Client private key (PEM)', { exact: true });
  }

  get certificateStatus() {
    return this.page.getByTestId('sso-certificate-status');
  }

  get clearCertificateButton() {
    return this.page.getByRole('button', { name: 'Clear client certificate', exact: true });
  }

  /** The confirmation a save that changes the issuer in force opens (#556). */
  get issuerChangeDialog() {
    return this.page.getByRole('dialog', { name: 'Change the issuer?' });
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
    return this.answerTo(() =>
      this.page.getByRole('button', { name: 'Save', exact: true }).click(),
    );
  }

  /** Presses "Change issuer" in the confirmation and returns the PUT it sends. */
  async confirmIssuerChange() {
    return this.answerTo(() =>
      this.issuerChangeDialog.getByRole('button', { name: 'Change issuer', exact: true }).click(),
    );
  }

  private async answerTo(action: () => Promise<void>) {
    const saved = this.page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname.endsWith('/settings/sso') &&
        response.request().method() === 'PUT',
    );
    await action();
    return saved;
  }
}
