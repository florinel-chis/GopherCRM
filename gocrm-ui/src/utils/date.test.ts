import { describe, it, expect } from 'vitest';
import { DATE_FALLBACK, formatDate, formatDistance, toDate } from './date';

describe('toDate', () => {
  it('returns null for missing values', () => {
    expect(toDate(null)).toBeNull();
    expect(toDate(undefined)).toBeNull();
    expect(toDate('')).toBeNull();
  });

  it('returns null for unparseable values', () => {
    expect(toDate('not-a-date')).toBeNull();
    expect(toDate(new Date('nope'))).toBeNull();
    expect(toDate(Number.NaN)).toBeNull();
  });

  it('parses ISO strings and passes Date instances through', () => {
    const parsed = toDate('2026-03-04T10:15:00Z');
    expect(parsed).toBeInstanceOf(Date);
    expect(parsed?.toISOString()).toBe('2026-03-04T10:15:00.000Z');

    const date = new Date('2026-03-04T10:15:00Z');
    expect(toDate(date)).toBe(date);
  });
});

describe('formatDate', () => {
  it('formats valid input with the given pattern', () => {
    expect(formatDate('2026-03-04T10:15:00Z', 'yyyy-MM-dd')).toBe('2026-03-04');
    expect(formatDate(new Date(2026, 2, 4), 'MMM dd, yyyy')).toBe('Mar 04, 2026');
  });

  it('returns the default fallback for missing input', () => {
    expect(formatDate(null, 'MMM dd, yyyy')).toBe(DATE_FALLBACK);
    expect(formatDate(undefined, 'MMM dd, yyyy')).toBe(DATE_FALLBACK);
  });

  it('returns the default fallback for invalid input', () => {
    expect(formatDate('not-a-date', 'MMM dd, yyyy')).toBe(DATE_FALLBACK);
  });

  it('honours a custom fallback', () => {
    expect(formatDate(null, 'MMM dd, yyyy', '')).toBe('');
    expect(formatDate('garbage', 'MMM dd, yyyy', 'n/a')).toBe('n/a');
  });

  it('never throws on invalid input', () => {
    expect(() => formatDate(undefined, 'MMM dd, yyyy HH:mm')).not.toThrow();
  });
});

describe('formatDistance', () => {
  it('describes valid input relative to now', () => {
    const inThreeDays = new Date(Date.now() + 3 * 24 * 60 * 60 * 1000);
    expect(formatDistance(inThreeDays)).toContain('days');
  });

  it('falls back for missing or invalid input', () => {
    expect(formatDistance(null)).toBe(DATE_FALLBACK);
    expect(formatDistance('not-a-date')).toBe(DATE_FALLBACK);
    expect(formatDistance(undefined, '')).toBe('');
  });
});
