import { describe, expect, it } from 'vitest';
import { FULL_PAGE_CHROME_STYLE, buildScreenshotOptions } from './screenshotCapture';

const file = '/tmp/docs/screenshots/customers/02-create.png';

/** The declarations of one rule, by selector, from the style string. */
function rule(selector: string): Record<string, string> {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`).exec(FULL_PAGE_CHROME_STYLE);
  if (!match) throw new Error(`no rule for ${selector}`);
  return Object.fromEntries(
    match[1]
      .split(';')
      .map((declaration) => declaration.trim())
      .filter(Boolean)
      .map((declaration) => {
        const [property, ...value] = declaration.split(':');
        return [property.trim(), value.join(':').trim()];
      })
  );
}

describe('buildScreenshotOptions (issue #187)', () => {
  it('viewport captures keep the plain options and no style', () => {
    const plain = { path: file, fullPage: false, animations: 'disabled', caret: 'hide' };
    expect(buildScreenshotOptions(file, {})).toEqual(plain);
    expect(buildScreenshotOptions(file, { fullPage: false })).toEqual(plain);
    expect(buildScreenshotOptions(file)).toEqual(plain);
  });

  it('full-page captures add the chrome stylesheet and nothing else', () => {
    const full = buildScreenshotOptions(file, { fullPage: true });
    expect(full).toEqual({
      path: file,
      fullPage: true,
      animations: 'disabled',
      caret: 'hide',
      style: FULL_PAGE_CHROME_STYLE,
    });
    expect(full.style).toBe(FULL_PAGE_CHROME_STYLE);
  });

  it('pins the app bar to the top of the document', () => {
    expect(rule('[data-testid="app-bar"]')).toEqual({
      position: 'absolute !important',
      top: '0 !important',
    });
  });

  it('anchors the permanent drawer paper in its nav box', () => {
    expect(rule('nav:has(> [data-testid="app-navigation"])')).toEqual({
      position: 'relative !important',
    });
    expect(rule('[data-testid="app-navigation"] > .MuiDrawer-paper')).toMatchObject({
      position: 'absolute !important',
      top: '0 !important',
      bottom: '0 !important',
    });
  });

  it('keeps every rule inside one screen media block', () => {
    const trimmed = FULL_PAGE_CHROME_STYLE.trim();
    expect(trimmed.startsWith('@media screen {')).toBe(true);
    expect(trimmed.endsWith('}')).toBe(true);
    expect(trimmed.match(/@media/g)).toHaveLength(1);
    const inner = trimmed.slice('@media screen {'.length, -1);
    expect(inner.includes('@media')).toBe(false);
  });

  it('never hides or masks the chrome', () => {
    expect(FULL_PAGE_CHROME_STYLE).not.toMatch(/display\s*:|visibility\s*:|opacity\s*:/);
    expect(FULL_PAGE_CHROME_STYLE).not.toMatch(/position\s*:\s*(fixed|sticky)/);
  });
});
