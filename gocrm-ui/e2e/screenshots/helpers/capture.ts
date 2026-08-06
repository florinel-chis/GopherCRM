import { Page } from '@playwright/test';
import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

/** Repo-level documentation folder the captures are published to. */
const screenshotRoot = path.resolve(
  fileURLToPath(new URL('.', import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  'docs',
  'screenshots'
);

export interface CaptureOptions {
  /** Capture the whole scrollable page rather than the viewport. */
  fullPage?: boolean;
}

/**
 * Saves a screenshot to docs/screenshots/<area>/<name>.png.
 *
 * Waits for the network to go quiet and gives MUI transitions a moment to
 * settle so captures are deterministic. Dialogs and menus should be captured
 * with the default viewport frame; long lists and detail pages read better
 * with `fullPage: true`.
 */
export async function capture(
  page: Page,
  area: string,
  name: string,
  options: CaptureOptions = {}
): Promise<string> {
  const dir = path.join(screenshotRoot, area);
  fs.mkdirSync(dir, { recursive: true });

  await page.waitForLoadState('networkidle');
  await page.waitForTimeout(300);

  const file = path.join(dir, `${name}.png`);
  await page.screenshot({
    path: file,
    fullPage: options.fullPage ?? false,
    animations: 'disabled',
    caret: 'hide',
  });
  return file;
}
