import { describe, it, expect } from 'vitest';
import {
  centsToDecimal,
  decimalToCents,
  formatCalendarDate,
  formatDealAmount,
  isPastCalendarDate,
} from './dealFormat';

describe('dealFormat', () => {
  it('formats cents as currency in the viewer locale', () => {
    const expected = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'EUR' }).format(12500);
    expect(formatDealAmount(1250000, 'EUR')).toBe(expected);
  });

  it('falls back to a plain number for a code Intl rejects', () => {
    expect(formatDealAmount(1999, 'XXXXX')).toBe('19.99 XXXXX');
  });

  it('renders a YYYY-MM-DD date on the stored day, whatever the zone', () => {
    expect(formatCalendarDate('2026-12-15')).toBe('Dec 15, 2026');
    expect(formatCalendarDate(null)).toBe('—');
  });

  it('compares past due against the local calendar day', () => {
    const today = new Date(2026, 9, 10, 23, 59);
    expect(isPastCalendarDate('2026-10-09', today)).toBe(true);
    expect(isPastCalendarDate('2026-10-10', today)).toBe(false);
    expect(isPastCalendarDate('2026-10-11', today)).toBe(false);
    expect(isPastCalendarDate(null, today)).toBe(false);
  });

  it('converts decimal text to cents without float drift', () => {
    expect(decimalToCents('1234.56')).toBe(123456);
    expect(decimalToCents('0.1')).toBe(10);
    expect(decimalToCents('19.9')).toBe(1990);
    expect(decimalToCents('4.35')).toBe(435);
    expect(decimalToCents('100')).toBe(10000);
    expect(decimalToCents('')).toBe(0);
  });

  it('renders cents back as two-decimal text', () => {
    expect(centsToDecimal(123456)).toBe('1234.56');
    expect(centsToDecimal(0)).toBe('0.00');
  });
});
