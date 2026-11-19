import { expect, type Page } from '@playwright/test';

/** What the test issuer's sign-in page lets a test choose. */
export interface IssuerIdentity {
  subject: string;
  email?: string;
  givenName?: string;
  familyName?: string;
  /** Comma-separated in the form; empty leaves the claim out. */
  roles?: string[];
  groups?: string[];
}

/**
 * The sign-in page of the OpenID Connect test issuer (scripts/e2e/oidc-issuer).
 * It shows the identity form only for a valid authorization request; a refused
 * one is a plain "Authorization request refused: <reason>" page.
 */
export class TestIssuerPage {
  constructor(readonly page: Page) {}

  get heading() {
    return this.page.getByRole('heading', { name: 'Test issuer sign-in' });
  }

  async expectSignInForm() {
    await expect(this.heading).toBeVisible({ timeout: 30 * 1000 });
  }

  async signIn(identity: IssuerIdentity) {
    await this.expectSignInForm();
    await this.page.getByLabel('Subject', { exact: true }).fill(identity.subject);
    await this.page.getByLabel('Email', { exact: true }).fill(identity.email ?? '');
    await this.page.getByLabel('Given name', { exact: true }).fill(identity.givenName ?? 'E2E');
    await this.page
      .getByLabel('Family name', { exact: true })
      .fill(identity.familyName ?? 'Person');
    await this.page.getByLabel('Roles', { exact: true }).fill((identity.roles ?? []).join(','));
    await this.page.getByLabel('Groups', { exact: true }).fill((identity.groups ?? []).join(','));
    await this.page.getByRole('button', { name: 'Sign in' }).click();
  }
}
