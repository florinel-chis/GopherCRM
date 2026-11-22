import { expect, type Locator, type Page, type Request } from '@playwright/test';

/**
 * Records every POST the page sends from now on to a path ending in
 * `pathSuffix` (for example `/customers`). Lookups such as `/users?search=`
 * or `/roles` are GETs or other paths and are not recorded.
 */
export function recordCreates(page: Page, pathSuffix: string): Request[] {
  const sent: Request[] = [];
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith(pathSuffix)) {
      sent.push(request);
    }
  });
  return sent;
}

/**
 * Submits the open entity form with every field empty and returns once the
 * form's own (zod) field messages can render.
 *
 * The forms mark their required inputs `required`, so the browser blocks the
 * first submit itself; that is asserted on `firstRequired`. The browser's
 * check is then switched off for this form only and the form is submitted
 * again, which runs the schema validation the field messages come from.
 */
export async function submitEmptyForm(page: Page, firstRequired: Locator, submit: Locator) {
  await submit.click();
  await expect
    .poll(() => firstRequired.evaluate((input: HTMLInputElement) => input.validity.valueMissing))
    .toBe(true);

  await page
    .locator('form')
    .filter({ has: submit })
    .evaluate((form: HTMLFormElement) => {
      form.noValidate = true;
    });
  await submit.click();
}
