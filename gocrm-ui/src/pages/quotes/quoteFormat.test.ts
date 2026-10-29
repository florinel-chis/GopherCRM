import { describe, it, expect } from 'vitest';
import {
  basisPointsToPercent,
  formatDiscountPercent,
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
});
