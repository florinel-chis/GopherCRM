import type { Page } from '@playwright/test';

/** Timeout for the first assertion after a sign-in, a navigation or a data load. */
export const LOADED = { timeout: 30 * 1000 };

/** Waits for the API response whose path matches `pathPattern` and whose request used `method`. */
export const apiResponse = (
  page: Page,
  pathPattern: RegExp,
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE',
) =>
  page.waitForResponse(
    (response) =>
      pathPattern.test(new URL(response.url()).pathname) && response.request().method() === method,
  );
