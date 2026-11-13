/**
 * Options for documentation screenshots (issue #187).
 *
 * Full-page captures stitch the page while the application chrome stays
 * fixed, so the app bar and the permanent navigation drawer were drawn at the
 * final scroll position, part-way down the image. For full-page captures only,
 * a screen-media stylesheet pins both to the top of the document so they render
 * once, in their natural place; the live application is untouched, and under
 * print media the chrome stays hidden as before.
 */

export interface ScreenshotCaptureOptions {
  /** Capture the whole scrollable page rather than the viewport. */
  fullPage?: boolean;
}

export interface ScreenshotOptions {
  path: string;
  fullPage: boolean;
  animations: 'disabled';
  caret: 'hide';
  style?: string;
}

/**
 * Screen-only rules applied for the duration of a full-page capture. The app
 * bar and the permanent drawer paper are taken out of the fixed layer and
 * anchored to the document's top; the drawer's `nav` box becomes the
 * positioning context so the paper keeps the drawer width. Nothing is hidden.
 */
export const FULL_PAGE_CHROME_STYLE = `@media screen {
  [data-testid="app-bar"] { position: absolute !important; top: 0 !important; }
  nav:has(> [data-testid="app-navigation"]) { position: relative !important; }
  [data-testid="app-navigation"] > .MuiDrawer-paper { position: absolute !important; top: 0 !important; bottom: 0 !important; height: auto !important; }
}`;

export function buildScreenshotOptions(
  file: string,
  options: ScreenshotCaptureOptions = {}
): ScreenshotOptions {
  const fullPage = options.fullPage ?? false;
  const base: ScreenshotOptions = { path: file, fullPage, animations: 'disabled', caret: 'hide' };
  return fullPage ? { ...base, style: FULL_PAGE_CHROME_STYLE } : base;
}
