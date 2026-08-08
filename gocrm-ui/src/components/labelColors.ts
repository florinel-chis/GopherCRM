/**
 * Colour helpers for task labels.
 *
 * Labels store a free-form `#RRGGBB` colour, so the chip has to work out its
 * own text colour at render time. The rule is the WCAG 2.x one: compute the
 * relative luminance of the background, then pick whichever of black or white
 * yields the higher contrast ratio.
 */

/**
 * Ten evenly-spaced hues used when a colour is assigned automatically (inline
 * label creation) and offered as swatches in the label dialog. Every entry
 * clears a 4.5:1 contrast ratio against black or white, whichever
 * `contrastingTextColor` picks for it.
 */
export const LABEL_COLOR_PALETTE = [
  '#1F77B4', // blue
  '#D62728', // red
  '#2CA02C', // green
  '#FF7F0E', // orange
  '#9467BD', // purple
  '#17A2B8', // teal
  '#E377C2', // pink
  '#8C564B', // brown
  '#BCBD22', // olive
  '#7F7F7F', // grey
] as const;

export const DEFAULT_LABEL_COLOR = LABEL_COLOR_PALETTE[0];

const HEX_COLOR = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i;

export const isValidHexColor = (color: string): boolean => HEX_COLOR.test(color.trim());

/** Splits `#rgb` or `#rrggbb` into 0-255 channels; null when unparseable. */
const parseHexColor = (color: string): [number, number, number] | null => {
  const value = color.trim();
  if (!HEX_COLOR.test(value)) {
    return null;
  }
  const digits = value.slice(1);
  const expanded =
    digits.length === 3
      ? digits
          .split('')
          .map((d) => d + d)
          .join('')
      : digits;
  return [
    parseInt(expanded.slice(0, 2), 16),
    parseInt(expanded.slice(2, 4), 16),
    parseInt(expanded.slice(4, 6), 16),
  ];
};

/** Linearizes one 0-255 sRGB channel, per WCAG 2.x. */
const channelLuminance = (channel: number): number => {
  const srgb = channel / 255;
  return srgb <= 0.03928 ? srgb / 12.92 : ((srgb + 0.055) / 1.055) ** 2.4;
};

/**
 * WCAG relative luminance of a hex colour, in [0, 1].
 * Unparseable input is treated as white so the chip degrades to dark text.
 */
export const relativeLuminance = (color: string): number => {
  const rgb = parseHexColor(color);
  if (!rgb) {
    return 1;
  }
  const [r, g, b] = rgb.map(channelLuminance);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

/** WCAG contrast ratio between two hex colours (1:1 to 21:1). */
export const contrastRatio = (a: string, b: string): number => {
  const lighter = Math.max(relativeLuminance(a), relativeLuminance(b));
  const darker = Math.min(relativeLuminance(a), relativeLuminance(b));
  return (lighter + 0.05) / (darker + 0.05);
};

/**
 * Black or white, whichever reads better on the given background.
 */
export const contrastingTextColor = (background: string): '#000000' | '#FFFFFF' => {
  const luminance = relativeLuminance(background);
  const contrastWithBlack = (luminance + 0.05) / 0.05;
  const contrastWithWhite = 1.05 / (luminance + 0.05);
  return contrastWithBlack >= contrastWithWhite ? '#000000' : '#FFFFFF';
};

/**
 * The next palette colour to hand out: the first one nobody is using yet, or —
 * once the palette is exhausted — the entry that keeps cycling through it.
 */
export const nextPaletteColor = (usedColors: string[]): string => {
  const used = new Set(usedColors.map((color) => color.trim().toUpperCase()));
  const unused = LABEL_COLOR_PALETTE.find((color) => !used.has(color));
  return unused ?? LABEL_COLOR_PALETTE[usedColors.length % LABEL_COLOR_PALETTE.length];
};
