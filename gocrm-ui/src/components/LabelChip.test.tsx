import { describe, it, expect } from 'vitest';
import { render, screen } from '@/test/test-utils';
import { LabelChip } from './LabelChip';
import {
  LABEL_COLOR_PALETTE,
  contrastRatio,
  contrastingTextColor,
  isValidHexColor,
  nextPaletteColor,
  relativeLuminance,
} from './labelColors';
import { createMockLabel } from '@/test/factories';

describe('relativeLuminance', () => {
  it('returns 0 for black and 1 for white', () => {
    expect(relativeLuminance('#000000')).toBeCloseTo(0, 6);
    expect(relativeLuminance('#FFFFFF')).toBeCloseTo(1, 6);
  });

  it('matches the WCAG values for the primary channels', () => {
    expect(relativeLuminance('#FF0000')).toBeCloseTo(0.2126, 4);
    expect(relativeLuminance('#00FF00')).toBeCloseTo(0.7152, 4);
    expect(relativeLuminance('#0000FF')).toBeCloseTo(0.0722, 4);
  });

  it('expands the three-digit shorthand form', () => {
    expect(relativeLuminance('#fff')).toBeCloseTo(relativeLuminance('#FFFFFF'), 6);
    expect(relativeLuminance('#f00')).toBeCloseTo(relativeLuminance('#FF0000'), 6);
  });

  it('is case-insensitive and tolerates surrounding whitespace', () => {
    expect(relativeLuminance(' #1f77b4 ')).toBeCloseTo(relativeLuminance('#1F77B4'), 6);
  });

  it('treats unparseable input as white so the chip falls back to dark text', () => {
    expect(relativeLuminance('not-a-color')).toBe(1);
    expect(contrastingTextColor('not-a-color')).toBe('#000000');
  });
});

describe('contrastingTextColor', () => {
  it('picks white on dark backgrounds and black on light ones', () => {
    expect(contrastingTextColor('#000000')).toBe('#FFFFFF');
    expect(contrastingTextColor('#1F77B4')).toBe('#FFFFFF');
    expect(contrastingTextColor('#FFFFFF')).toBe('#000000');
    expect(contrastingTextColor('#BCBD22')).toBe('#000000');
  });

  it('always picks the higher-contrast option of black and white', () => {
    for (const color of ['#000000', '#FFFFFF', '#808080', '#123456', '#ABCDEF', '#FF7F0E']) {
      const chosen = contrastingTextColor(color);
      const other = chosen === '#000000' ? '#FFFFFF' : '#000000';
      expect(contrastRatio(color, chosen)).toBeGreaterThanOrEqual(contrastRatio(color, other));
    }
  });

  it('clears the 4.5:1 WCAG AA threshold for every palette colour', () => {
    for (const color of LABEL_COLOR_PALETTE) {
      expect(contrastRatio(color, contrastingTextColor(color))).toBeGreaterThanOrEqual(4.5);
    }
  });
});

describe('isValidHexColor', () => {
  it('accepts three- and six-digit hex values and rejects anything else', () => {
    expect(isValidHexColor('#fff')).toBe(true);
    expect(isValidHexColor('#1F77B4')).toBe(true);
    expect(isValidHexColor('1F77B4')).toBe(false);
    expect(isValidHexColor('#12345')).toBe(false);
    expect(isValidHexColor('rebeccapurple')).toBe(false);
  });
});

describe('nextPaletteColor', () => {
  it('hands out the first unused palette colour', () => {
    expect(nextPaletteColor([])).toBe(LABEL_COLOR_PALETTE[0]);
    expect(nextPaletteColor([LABEL_COLOR_PALETTE[0]])).toBe(LABEL_COLOR_PALETTE[1]);
  });

  it('ignores case when deciding which colours are taken', () => {
    expect(nextPaletteColor([LABEL_COLOR_PALETTE[0].toLowerCase()])).toBe(LABEL_COLOR_PALETTE[1]);
  });

  it('cycles once every palette colour is in use', () => {
    const allUsed = [...LABEL_COLOR_PALETTE];
    expect(nextPaletteColor(allUsed)).toBe(LABEL_COLOR_PALETTE[0]);
    expect(nextPaletteColor([...allUsed, LABEL_COLOR_PALETTE[0]])).toBe(LABEL_COLOR_PALETTE[1]);
  });
});

describe('LabelChip', () => {
  it('renders the label name', () => {
    render(<LabelChip label={createMockLabel({ id: 2, name: 'Billing' })} />);

    expect(screen.getByText('Billing')).toBeInTheDocument();
  });

  it('paints the chip in the label colour with contrasting text', () => {
    render(<LabelChip label={createMockLabel({ id: 3, name: 'Dark', color: '#000000' })} />);

    const chip = screen.getByTestId('label-chip-3');
    const styles = window.getComputedStyle(chip);
    expect(styles.backgroundColor).toBe('rgb(0, 0, 0)');
    expect(styles.color).toBe('rgb(255, 255, 255)');
  });

  it('uses dark text on a light label colour', () => {
    render(<LabelChip label={createMockLabel({ id: 4, name: 'Light', color: '#FFFFFF' })} />);

    const styles = window.getComputedStyle(screen.getByTestId('label-chip-4'));
    expect(styles.backgroundColor).toBe('rgb(255, 255, 255)');
    expect(styles.color).toBe('rgb(0, 0, 0)');
  });
});
