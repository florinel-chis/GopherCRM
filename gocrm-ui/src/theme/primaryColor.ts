import { decomposeColor, getContrastRatio } from '@mui/material/styles';

/** The shipped primary colour, kept whenever a stored value is unusable. */
export const DEFAULT_PRIMARY_COLOR = '#1976d2';

const NUMBER = '[+-]?(?:\\d+(?:\\.\\d*)?|\\.\\d+)';

const HEX = /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i;
// MUI's decomposeColor splits on commas, so only the comma syntax is accepted.
const RGB = new RegExp(
  `^rgba?\\(\\s*(${NUMBER})\\s*,\\s*(${NUMBER})\\s*,\\s*(${NUMBER})\\s*(?:,\\s*(${NUMBER})\\s*)?\\)$`,
  'i',
);
const HSL = new RegExp(
  `^hsla?\\(\\s*(${NUMBER})\\s*,\\s*(${NUMBER})%\\s*,\\s*(${NUMBER})%\\s*(?:,\\s*(${NUMBER})\\s*)?\\)$`,
  'i',
);

const inRange = (value: string | undefined, min: number, max: number) =>
  value === undefined || (Number(value) >= min && Number(value) <= max);

const hasSupportedFormat = (value: string): boolean => {
  if (HEX.test(value)) return true;

  const rgb = RGB.exec(value);
  if (rgb) {
    return (
      inRange(rgb[1], 0, 255) &&
      inRange(rgb[2], 0, 255) &&
      inRange(rgb[3], 0, 255) &&
      inRange(rgb[4], 0, 1)
    );
  }

  const hsl = HSL.exec(value);
  if (hsl) {
    return (
      inRange(hsl[1], 0, 360) &&
      inRange(hsl[2], 0, 100) &&
      inRange(hsl[3], 0, 100) &&
      inRange(hsl[4], 0, 1)
    );
  }

  // Named colours, var(...), 4/8-digit hex, color(), oklch() and anything else.
  return false;
};

/**
 * The primary colour to build the theme from: the trimmed stored value when it
 * is a 3- or 6-digit hex, rgb()/rgba() or hsl()/hsla() colour that MUI can
 * decompose and compute a contrast for, otherwise DEFAULT_PRIMARY_COLOR. Never
 * throws.
 */
export const resolvePrimaryColor = (value: unknown): string => {
  if (typeof value !== 'string') return DEFAULT_PRIMARY_COLOR;
  const trimmed = value.trim();
  if (trimmed === '' || !hasSupportedFormat(trimmed)) return DEFAULT_PRIMARY_COLOR;

  try {
    const decomposed = decomposeColor(trimmed);
    if (!decomposed.values.every((channel) => Number.isFinite(channel))) {
      return DEFAULT_PRIMARY_COLOR;
    }
    if (!Number.isFinite(getContrastRatio(trimmed, '#ffffff'))) return DEFAULT_PRIMARY_COLOR;
  } catch {
    return DEFAULT_PRIMARY_COLOR;
  }
  return trimmed;
};

/** True when the colour is the shipped default (compared case-insensitively). */
export const isDefaultPrimaryColor = (colour: string): boolean =>
  colour.toLowerCase() === DEFAULT_PRIMARY_COLOR;
