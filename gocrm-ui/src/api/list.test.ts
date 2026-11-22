import { describe, expect, it } from 'vitest';
import type { ApiResponseWithMeta } from './client';
import { toListResult, withoutUnset } from './list';

const response = <T>(data: unknown, total?: number): ApiResponseWithMeta<T[]> =>
  ({
    success: true,
    data,
    ...(total === undefined ? {} : { meta: { total } }),
  }) as unknown as ApiResponseWithMeta<T[]>;

describe('toListResult', () => {
  it('gives no rows when data is not an array', () => {
    expect(toListResult(response<number>(null, 3))).toEqual({ rows: [], total: 3 });
    expect(toListResult(response<number>({ id: 1 }))).toEqual({ rows: [], total: 0 });
  });

  it('falls back to the number of rows without meta.total', () => {
    expect(toListResult(response<number>([1, 2]))).toEqual({ rows: [1, 2], total: 2 });
  });

  it('takes meta.total when it is present', () => {
    expect(toListResult(response<number>([1, 2], 40))).toEqual({ rows: [1, 2], total: 40 });
  });
});

describe('withoutUnset', () => {
  it("drops undefined and '' and keeps 0 and false", () => {
    expect(
      withoutUnset({ a: undefined, b: '', c: 0, d: false, e: 'x', offset: 0, limit: 20 }),
    ).toEqual({ c: 0, d: false, e: 'x', offset: 0, limit: 20 });
  });
});
