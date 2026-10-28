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

export const formatDiscountPercent = (basisPoints: number): string =>
  `${basisPointsToPercent(basisPoints)}%`;

/** The customer's display name; the API never sends the contact details. */
export const quoteCustomerName = (customer: QuoteCustomerSummary | undefined): string =>
  customer ? `${customer.first_name} ${customer.last_name}`.trim() : '';

export const quoteOwnerName = (owner: QuoteOwnerSummary | undefined, ownerId: number): string =>
  owner ? `${owner.first_name} ${owner.last_name}`.trim() : `User #${ownerId}`;
