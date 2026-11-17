import { decimalToCents, formatDealAmount } from '@/pages/deals/dealFormat';
import type { QuoteCustomerSummary, QuoteOwnerSummary } from '@/types';

/**
 * Integer minor units with the quote's ISO 4217 code, the deal convention.
 * Totals are always the server's; this only renders them.
 */
export const formatMoney = formatDealAmount;

/**
 * A discount typed as a percentage ("12.5") to basis points (1250), exactly:
 * the same integer split as decimalToCents, since 1% is 100 bp just as one
 * major unit is 100 cents.
 */
export const percentToBasisPoints = (value: string): number => decimalToCents(value);

/** Basis points to the percentage text the form edits: 1250 → "12.5", 1000 → "10". */
export const basisPointsToPercent = (basisPoints: number): string =>
  (basisPoints / 100).toFixed(2).replace(/\.?0+$/, '');

/** The smallest and largest line quantity in thousandths, the API's bounds (#343). */
export const QUOTE_QUANTITY_MILLI_MIN = 1;
export const QUOTE_QUANTITY_MILLI_MAX = 1_000_000_000;

const QUANTITY_PATTERN = /^(\d+)(?:\.(\d{1,3}))?$/;

/**
 * Whether the typed quantity is a plain decimal (dot separator, no sign, no
 * exponent, no thousands separator) from 0.001 to 1,000,000 with at most three
 * decimals. Surrounding whitespace is ignored. The check is done on the digits,
 * in integer thousandths, so no float rounding can let a value past a bound.
 */
export const isValidQuoteQuantity = (text: string): boolean => {
  const match = QUANTITY_PATTERN.exec(text.trim());
  if (!match) {
    return false;
  }
  const whole = match[1].replace(/^0+(?=\d)/, '');
  // More than seven integer digits is above 1,000,000 whatever follows.
  if (whole.length > 7) {
    return false;
  }
  const fraction = (match[2] ?? '').padEnd(3, '0');
  const milli = Number(whole) * 1000 + Number(fraction);
  return milli >= QUOTE_QUANTITY_MILLI_MIN && milli <= QUOTE_QUANTITY_MILLI_MAX;
};

/**
 * A line quantity as the shortest plain decimal rounded to three decimals:
 * 1.5 → "1.5", 2 → "2", 0.001 → "0.001", 1000000 → "1000000". No trailing
 * zeros, no exponent, no thousands separator.
 */
export const formatQuantity = (quantity: number): string => {
  const fixed = quantity.toFixed(3);
  if (!fixed.includes('.')) {
    return fixed;
  }
  return fixed.replace(/0+$/, '').replace(/\.$/, '');
};

export const formatDiscountPercent =(basisPoints: number): string =>
  `${basisPointsToPercent(basisPoints)}%`;

/** The customer's display name; the API never sends the contact details. */
export const quoteCustomerName = (customer: QuoteCustomerSummary | undefined): string =>
  customer ? `${customer.first_name} ${customer.last_name}`.trim() : '';

export const quoteOwnerName = (owner: QuoteOwnerSummary | undefined, ownerId: number): string =>
  owner ? `${owner.first_name} ${owner.last_name}`.trim() : `User #${ownerId}`;
