import { useEffect, useState } from 'react';
import { keepPreviousData, useQuery, type QueryKey } from '@tanstack/react-query';
import type { ListResult } from '@/api/list';

export interface PageWindow {
  offset: number;
  limit: number;
}

export interface UsePagedListOptions<T> {
  /** The key and the request of one page; both take the window the hook holds. */
  queryKey: (window: PageWindow) => QueryKey;
  queryFn: (window: PageWindow) => Promise<ListResult<T>>;
  staleTime?: number;
  initialRowsPerPage?: number;
}

/**
 * One paged list: the page and rows-per-page state, the query of the current
 * page, and the move back to the last page that exists. While the next page
 * loads, the rows of the previous one stay on screen (keepPreviousData), so
 * isLoading is true only on the first load.
 */
export const usePagedList = <T>({
  queryKey,
  queryFn,
  staleTime,
  initialRowsPerPage = 20,
}: UsePagedListOptions<T>) => {
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(initialRowsPerPage);
  const pageWindow: PageWindow = { offset: page * rowsPerPage, limit: rowsPerPage };

  const query = useQuery({
    queryKey: queryKey(pageWindow),
    queryFn: () => queryFn(pageWindow),
    placeholderData: keepPreviousData,
    ...(staleTime === undefined ? {} : { staleTime }),
  });

  const rows = query.data?.rows ?? [];
  const total = query.data?.total ?? 0;
  const lastPage = Math.max(0, Math.ceil(total / rowsPerPage) - 1);
  // An empty page of a list that still has rows (rows were pruned or deleted
  // after the page was chosen): move to the last page that exists. Only ever
  // backwards, so an answer that contradicts itself cannot loop. Judged only
  // on the answer for the current page, never on the previous page's rows
  // shown while it loads.
  const pastLastPage =
    !query.isPlaceholderData && rows.length === 0 && total > 0 && page > lastPage;

  useEffect(() => {
    if (pastLastPage) {
      setPage(lastPage);
    }
  }, [pastLastPage, lastPage]);

  return {
    isLoading: query.isLoading,
    isError: query.isError,
    error: query.error,
    isFetching: query.isFetching,
    isPlaceholderData: query.isPlaceholderData,
    rows,
    total,
    page,
    /** The page to show in the pagination control, never past the end. */
    displayPage: Math.min(page, lastPage),
    rowsPerPage,
    onPageChange: (_event: unknown, next: number) => setPage(next),
    onRowsPerPageChange: (event: { target: { value: string | number } }) => {
      setRowsPerPage(Number(event.target.value));
      setPage(0);
    },
    resetPage: () => setPage(0),
  };
};
