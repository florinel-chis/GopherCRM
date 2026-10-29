import { describe, it, expect } from 'vitest';
import { centsToMoneyText, parseConditionNumber, parseMoneyToCents } from './conditionValues';

describe('conditionValues', () => {
  it('converts money text to exact cents: -1.50, 1.5, 0.05, 12, -0.01', () => {
    expect(parseMoneyToCents('-1.50')).toBe(-150);
    expect(parseMoneyToCents('1.5')).toBe(150);
    expect(parseMoneyToCents('0.05')).toBe(5);
    expect(parseMoneyToCents('12')).toBe(1200);
    expect(parseMoneyToCents('-0.01')).toBe(-1);
    // Surrounding whitespace is trimmed; float multiplication would give 1155.0000000000002 here.
    expect(parseMoneyToCents(' 11.55 ')).toBe(1155);
    expect(parseMoneyToCents('0')).toBe(0);
  });

  it('rejects exponents, plus signs, separators, three decimals, bare fractions and empty text', () => {
    for (const text of ['1e3', '+1', '1,000', '1.000,50', '1 000', '1.234', '.5', '', '   ', '-', '1.', 'abc']) {
      expect(parseMoneyToCents(text), text).toBeNull();
    }
    // Beyond Number.MAX_SAFE_INTEGER cents.
    expect(parseMoneyToCents('90071992547409.92')).toBeNull();
    expect(parseMoneyToCents('90071992547409.91')).toBe(Number.MAX_SAFE_INTEGER);
  });

  it('formats integer cents back to money text', () => {
    expect(centsToMoneyText(-150)).toBe('-1.50');
    expect(centsToMoneyText(5)).toBe('0.05');
    expect(centsToMoneyText(1200)).toBe('12.00');
    expect(centsToMoneyText(-1)).toBe('-0.01');
    expect(centsToMoneyText(0)).toBe('0.00');
    expect(parseMoneyToCents(centsToMoneyText(150050))).toBe(150050);
  });

  it('parses plain numbers and rejects exponents and separators', () => {
    expect(parseConditionNumber('10')).toBe(10);
    expect(parseConditionNumber('12.5')).toBe(12.5);
    expect(parseConditionNumber('-3')).toBe(-3);
    expect(parseConditionNumber(' 7 ')).toBe(7);
    for (const text of ['1e3', '+1', '1,000', '1 000', '.5', '', '5.', 'x']) {
      expect(parseConditionNumber(text), text).toBeNull();
    }
  });
});
