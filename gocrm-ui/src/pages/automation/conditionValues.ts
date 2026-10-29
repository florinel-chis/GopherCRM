// Text <-> JSON value conversion for condition values. The server compares
// amount_cents against deals.amount_cents and accepts any JSON number
// (negatives and fractions included), so the syntax here is defined
// narrowly and converted without floating-point multiplication.

const MONEY_PATTERN = /^(-?)(\d+)(?:\.(\d{1,2}))?$/;
const NUMBER_PATTERN = /^-?\d+(\.\d+)?$/;

/**
 * Major-unit money text to integer cents: optional leading minus, digits, at
 * most two decimals. Exponents, plus signs, thousands or locale separators,
 * a bare ".5" and empty text give null, as does a result beyond
 * Number.MAX_SAFE_INTEGER.
 */
export const parseMoneyToCents = (text: string): number | null => {
  const match = MONEY_PATTERN.exec(text.trim());
  if (!match) return null;
  const [, sign, whole, fraction = ''] = match;
  const digits = `${whole}${fraction.padEnd(2, '0')}`.replace(/^0+(?=\d)/, '');
  const cents = Number(digits);
  if (!Number.isSafeInteger(cents)) return null;
  return sign && cents !== 0 ? -cents : cents;
};

/** Integer cents to money text with two decimals: -150 -> "-1.50". */
export const centsToMoneyText = (cents: number): string => {
  const negative = cents < 0;
  const digits = String(Math.abs(cents)).padStart(3, '0');
  const text = `${digits.slice(0, -2)}.${digits.slice(-2)}`;
  return negative ? `-${text}` : text;
};

/** Plain decimal numbers (owner_id, probability); exponents and separators give null. */
export const parseConditionNumber = (text: string): number | null => {
  const trimmed = text.trim();
  if (!NUMBER_PATTERN.test(trimmed)) return null;
  const value = Number(trimmed);
  return Number.isFinite(value) ? value : null;
};
