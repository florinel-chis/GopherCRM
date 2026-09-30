import { format, parseISO } from 'date-fns';
import { formatDate, DATE_FALLBACK } from '@/utils/date';

/**
 * Formats integer minor units with the deal's ISO 4217 code in the viewer's
 * locale. An unknown code makes Intl throw; the fallback keeps the page up.
 */
export const formatDealAmount = (amountCents: number, currency: string): string => {
  const amount = amountCents / 100;
  try {
    return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount);
  } catch {
    return `${amount.toFixed(2)} ${currency}`;
  }
};

/**
 * A calendar date the API exchanges as YYYY-MM-DD. `parseISO` reads a
 * date-only string as local midnight (the native Date constructor would read
 * it as UTC and shift the day west of Greenwich), so the day shown is the day
 * stored.
 */
export const formatCalendarDate = (value: string | null | undefined, pattern = 'MMM dd, yyyy'): string => {
  if (!value) {
    return DATE_FALLBACK;
  }
  return formatDate(parseISO(value), pattern);
};

/**
 * True when the YYYY-MM-DD date is before today's local calendar date. The two
 * strings compare lexically, so no timezone conversion is involved.
 */
export const isPastCalendarDate = (value: string | null | undefined, today: Date = new Date()): boolean =>
  !!value && value < format(today, 'yyyy-MM-dd');

/**
 * Decimal amount typed by the user ("1234.5") to integer cents, exactly:
 * the parts are handled as integers, never as a float product.
 */
export const decimalToCents = (value: string): number => {
  const trimmed = value.trim();
  if (trimmed === '') {
    return 0;
  }
  const [whole, fraction = ''] = trimmed.split('.');
  const cents = (fraction + '00').slice(0, 2);
  return parseInt(whole || '0', 10) * 100 + parseInt(cents, 10);
};

/** Integer cents to the decimal text the form edits. */
export const centsToDecimal = (amountCents: number): string => (amountCents / 100).toFixed(2);
