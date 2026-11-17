import { describe, it, expect } from 'vitest';
import {
  basisPointsToPercent,
  formatDiscountPercent,
  formatQuantity,
  isValidQuoteQuantity,
  percentToBasisPoints,
  quoteCustomerName,
  quoteOwnerName,
} from './quoteFormat';

describe('quoteFormat', () => {
  it('converts a percentage to basis points exactly', () => {
    expect(percentToBasisPoints('12.5')).toBe(1250);
    expect(percentToBasisPoints('12.50')).toBe(1250);
    expect(percentToBasisPoints('10')).toBe(1000);
    expect(percentToBasisPoints('0')).toBe(0);
    expect(percentToBasisPoints('')).toBe(0);
    expect(percentToBasisPoints('100')).toBe(10000);
  });

  it('renders basis points as a percentage without trailing zeros', () => {
    expect(basisPointsToPercent(1250)).toBe('12.5');
    expect(basisPointsToPercent(1000)).toBe('10');
    expect(basisPointsToPercent(0)).toBe('0');
    expect(basisPointsToPercent(10000)).toBe('100');
    expect(basisPointsToPercent(1)).toBe('0.01');
    expect(formatDiscountPercent(1250)).toBe('12.5%');
  });

  it('names the customer and the owner from the summaries', () => {
    expect(quoteCustomerName({ id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme' })).toBe('Jane Smith');
    expect(quoteCustomerName(undefined)).toBe('');
    expect(quoteOwnerName({ id: 1, first_name: 'Ana', last_name: 'Pop' }, 1)).toBe('Ana Pop');
    expect(quoteOwnerName(undefined, 4)).toBe('User #4');
  });

  it('formats a quantity without trailing zeros', () => {
    expect(formatQuantity(1.5)).toBe('1.5');
    expect(formatQuantity(2)).toBe('2');
    expect(formatQuantity(0.001)).toBe('0.001');
    expect(formatQuantity(1.25)).toBe('1.25');
    expect(formatQuantity(1000000)).toBe('1000000');
    expect(formatQuantity(999999.999)).toBe('999999.999');
    expect(formatQuantity(10)).toBe('10');
    expect(formatQuantity(100.1)).toBe('100.1');
    expect(formatQuantity(0.125)).toBe('0.125');
    // Rounded to three decimals, never an exponent.
    expect(formatQuantity(2.0004)).toBe('2');
    expect(formatQuantity(0.0015)).not.toMatch(/e/i);
  });

  it('validates quantity text from 0.001 to 1000000 with at most three decimals', () => {
    for (const text of ['0.001', '1', ' 1.5 ', '123456.789', '999999.999', '1000000', '1000000.000']) {
      expect(isValidQuoteQuantity(text), text).toBe(true);
    }
    for (const text of [
      '',
      '0',
      '0.000',
      '0.0009',
      '0.0001',
      '1.2345',
      '1.5000',
      '1000000.001',
      '1000000.0001',
      '1000001',
      '-1',
      '+1',
      '.5',
      '1.',
      '1e3',
      '1,5',
      'abc',
      'Infinity',
      'NaN',
      '   ',
      '99999999',
    ]) {
      expect(isValidQuoteQuantity(text), JSON.stringify(text)).toBe(false);
    }
  });

  it('keeps a three-decimal quantity exact through Number and JSON', () => {
    const cases: Array<[string, string]> = [
      ['0.001', '0.001'],
      ['999999.999', '999999.999'],
      ['1000000', '1000000'],
      ['123456.789', '123456.789'],
      ['1000000.000', '1000000'],
    ];
    for (const [text, expected] of cases) {
      expect(isValidQuoteQuantity(text), text).toBe(true);
      expect(JSON.stringify(Number(text.trim()))).toBe(expected);
      expect(formatQuantity(Number(text.trim()))).toBe(expected);
    }
  });
});
