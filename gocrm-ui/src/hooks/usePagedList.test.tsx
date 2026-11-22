import { describe, it, expect, vi } from 'vitest';
import { createElement, type ReactNode } from 'react';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ListResult } from '@/api/list';
import { usePagedList, type PageWindow } from './usePagedList';

type Answer = ListResult<number>;

const pageOf = (offset: number, count: number): number[] =>
  Array.from({ length: count }, (_unused, index) => offset + index + 1);

// Answers by offset; an offset without an answer is held pending until released.
const server = (answers: Record<number, Answer>) => {
  const pending = new Map<number, (answer: Answer) => void>();
  const queryFn = vi.fn(
    (window: PageWindow): Promise<Answer> =>
      window.offset in answers
        ? Promise.resolve(answers[window.offset])
        : new Promise((resolve) => pending.set(window.offset, resolve)),
  );
  const release = (offset: number, answer: Answer) => {
    const resolve = pending.get(offset);
    if (!resolve) throw new Error(`no pending request at offset ${offset}`);
    resolve(answer);
  };
  return { queryFn, release };
};

const renderPagedList = (queryFn: (window: PageWindow) => Promise<Answer>) => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return renderHook(
    () =>
      usePagedList({
        queryKey: (window) => ['paged-list-test', window],
        queryFn,
      }),
    { wrapper },
  );
};

describe('usePagedList', () => {
  it('moves to the last existing page, not the first, when a page past the end comes back empty', async () => {
    // Rows were pruned: page 2 is gone and the total now ends on page 1.
    const { queryFn } = server({
      0: { rows: pageOf(0, 20), total: 45 },
      20: { rows: pageOf(20, 5), total: 25 },
      40: { rows: [], total: 25 },
    });
    const { result } = renderPagedList(queryFn);
    await waitFor(() => expect(result.current.rows).toHaveLength(20));

    act(() => result.current.onPageChange(null, 2));
    await waitFor(() => expect(queryFn).toHaveBeenCalledWith({ offset: 40, limit: 20 }));

    await waitFor(() => expect(result.current.page).toBe(1));
    await waitFor(() => expect(result.current.rows).toEqual(pageOf(20, 5)));
    expect(result.current.total).toBe(25);
    expect(queryFn).toHaveBeenLastCalledWith({ offset: 20, limit: 20 });
  });

  it('keeps the previous rows while the next page is pending, without a first-load state', async () => {
    const { queryFn, release } = server({ 0: { rows: pageOf(0, 20), total: 45 } });
    const { result } = renderPagedList(queryFn);
    await waitFor(() => expect(result.current.rows).toHaveLength(20));
    expect(result.current.isLoading).toBe(false);

    act(() => result.current.onPageChange(null, 1));
    await waitFor(() => expect(queryFn).toHaveBeenCalledWith({ offset: 20, limit: 20 }));
    expect(result.current.page).toBe(1);
    expect(result.current.rows).toEqual(pageOf(0, 20));
    expect(result.current.isPlaceholderData).toBe(true);
    expect(result.current.isLoading).toBe(false);

    act(() => release(20, { rows: pageOf(20, 20), total: 45 }));
    await waitFor(() => expect(result.current.rows).toEqual(pageOf(20, 20)));
    expect(result.current.isPlaceholderData).toBe(false);
  });

  it('does not move the page on an empty placeholder that would be past the end', async () => {
    // The first answer contradicts itself (no rows, a total of 25). Shown as
    // the placeholder of page 3, it would put page 3 past the last page 1.
    const { queryFn, release } = server({ 0: { rows: [], total: 25 } });
    const { result } = renderPagedList(queryFn);
    await waitFor(() => expect(result.current.total).toBe(25));
    expect(result.current.page).toBe(0);

    act(() => result.current.onPageChange(null, 3));
    await waitFor(() => expect(queryFn).toHaveBeenCalledWith({ offset: 60, limit: 20 }));
    expect(result.current.isPlaceholderData).toBe(true);
    expect(result.current.rows).toEqual([]);
    expect(result.current.page).toBe(3);

    act(() => release(60, { rows: pageOf(60, 5), total: 65 }));
    await waitFor(() => expect(result.current.rows).toEqual(pageOf(60, 5)));
    expect(result.current.page).toBe(3);
    expect(queryFn).toHaveBeenCalledTimes(2);
  });

  it('does not move the page on an empty list', async () => {
    const { queryFn } = server({
      0: { rows: pageOf(0, 20), total: 45 },
      40: { rows: [], total: 0 },
    });
    const { result } = renderPagedList(queryFn);
    await waitFor(() => expect(result.current.rows).toHaveLength(20));

    act(() => result.current.onPageChange(null, 2));
    await waitFor(() => expect(result.current.isPlaceholderData).toBe(false));
    await waitFor(() => expect(result.current.total).toBe(0));
    expect(result.current.rows).toEqual([]);
    expect(result.current.page).toBe(2);
    expect(queryFn).toHaveBeenCalledTimes(2);
  });

  it('goes back to the first page when the rows per page change', async () => {
    const { queryFn } = server({
      0: { rows: pageOf(0, 20), total: 100 },
      40: { rows: pageOf(40, 20), total: 100 },
    });
    const { result } = renderPagedList(queryFn);
    await waitFor(() => expect(result.current.rows).toHaveLength(20));

    act(() => result.current.onPageChange(null, 2));
    await waitFor(() => expect(result.current.rows).toEqual(pageOf(40, 20)));

    act(() => result.current.onRowsPerPageChange({ target: { value: '50' } }));
    expect(result.current.page).toBe(0);
    expect(result.current.rowsPerPage).toBe(50);
    await waitFor(() => expect(queryFn).toHaveBeenLastCalledWith({ offset: 0, limit: 50 }));
  });
});
