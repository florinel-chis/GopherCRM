import { describe, it, expect } from 'vitest';
import { DEFAULT_PRIMARY_COLOR, resolvePrimaryColor } from './primaryColor';

describe('resolvePrimaryColor', () => {
  it('accepts hex, rgb(a) and hsl(a) colours', () => {
    const accepted = [
      '#ff5722',
      '#FF5722',
      '#f52',
      'rgb(255, 87, 34)',
      'rgba(255,87,34,0.9)',
      'hsl(14, 100%, 57%)',
      'hsla(14,100%,57%,1)',
      '#1976d2',
    ];
    for (const colour of accepted) {
      expect(resolvePrimaryColor(colour)).toBe(colour);
    }
    // Surrounding whitespace is trimmed, not a reason to refuse.
    expect(resolvePrimaryColor(' #ff5722 ')).toBe('#ff5722');
    expect(resolvePrimaryColor('\trgb(0, 0, 0)\n')).toBe('rgb(0, 0, 0)');
  });

  it('falls back to #1976d2 for empty, named, variable and malformed values', () => {
    expect(DEFAULT_PRIMARY_COLOR).toBe('#1976d2');
    const refused: unknown[] = [
      undefined,
      null,
      42,
      '',
      '   ',
      'red',
      'dimgray',
      'transparent',
      'var(--brand)',
      '#ff57',
      '#ff572280',
      'color(srgb 1 0 0)',
      'oklch(0.6 0.2 30)',
      '#zzz',
      '#ff572',
      'ff5722',
      '58, 53, 65',
      'rgb(a,b,c)',
      'rgb(255 87 34)',
      'rgb(300, 0, 0)',
      'rgba(255, 87, 34, 2)',
      'hsl(14, 100, 57)',
      'hsl(14, 140%, 57%)',
      'rgb(255, 87, 34); color: red',
    ];
    for (const value of refused) {
      expect(() => resolvePrimaryColor(value)).not.toThrow();
      expect(resolvePrimaryColor(value)).toBe('#1976d2');
    }
  });
});
