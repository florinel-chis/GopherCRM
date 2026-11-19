import { format, formatDistanceToNow, isValid } from 'date-fns';

/**
 * Placeholder rendered when a date is missing or unparseable.
 */
export const DATE_FALLBACK = '—';

export type DateInput = string | number | Date | null | undefined;

/**
 * Normalises an API value into a usable Date, or null when the value is
 * missing or cannot be parsed. Optional API fields (`*time.Time` on the Go
 * side) arrive as null/undefined and must never reach date-fns directly:
 * `format()` throws `RangeError: Invalid time value` and takes the page with it.
 */
export const toDate = (value: DateInput): Date | null => {
  if (value === null || value === undefined || value === '') {
    return null;
  }
  const date = value instanceof Date ? value : new Date(value);
  return isValid(date) ? date : null;
};

/**
 * Formats a date with the given date-fns pattern, returning `fallback` for
 * missing or invalid input instead of throwing.
 */
export const formatDate = (
  value: DateInput,
  pattern: string,
  fallback: string = DATE_FALLBACK
): string => {
  const date = toDate(value);
  return date ? format(date, pattern) : fallback;
};

/**
 * Relative distance from now ("in 3 days"), guarded the same way as formatDate.
 */
export const formatDistance = (
  value: DateInput,
  fallback: string = DATE_FALLBACK
): string => {
  const date = toDate(value);
  return date ? formatDistanceToNow(date, { addSuffix: true }) : fallback;
};

/**
 * A datetime-local value ("2026-11-20T09:30") is a time in the browser's
 * zone; the API takes RFC 3339. Answers the UTC instant, or undefined for an
 * empty or unparseable value.
 */
export const localDateTimeToUTC = (value: string): string | undefined => {
  if (!value) {
    return undefined;
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return undefined;
  }
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z');
};
