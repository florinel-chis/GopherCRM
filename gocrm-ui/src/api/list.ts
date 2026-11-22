import type { ApiResponseWithMeta } from './client';

/** One page of a paged list and the size of the whole list. */
export interface ListResult<T> {
  rows: T[];
  total: number;
}

/** A paged answer as rows and a total; without meta.total the page's own size is the total. */
export const toListResult = <T>(response: ApiResponseWithMeta<T[]>): ListResult<T> => {
  const rows = Array.isArray(response.data) ? response.data : [];
  return { rows, total: response.meta?.total ?? rows.length };
};

// The API refuses an unknown status or event type, so an unset filter is left
// out of the query instead of being sent empty.
export const withoutUnset = <T extends object>(params: T): Partial<T> =>
  Object.fromEntries(
    Object.entries(params).filter(([, value]) => value !== undefined && value !== ''),
  ) as Partial<T>;
