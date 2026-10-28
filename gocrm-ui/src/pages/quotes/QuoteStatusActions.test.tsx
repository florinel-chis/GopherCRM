import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { QuoteStatusActions } from './QuoteStatusActions';
import { quotesApi } from '@/api/endpoints';
import { createMockQuote } from '@/test/factories';

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    changeStatus: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

// The rejection the component sees: the API answers
// `{success:false, error:{code, message, details}, meta}` and the axios
// client replaces `response.data` with that `error` object before rethrowing.
const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const ALL_LABELS = ['Mark as sent', 'Accept', 'Reject', 'Mark expired', 'Revise'];

const offeredLabels = () => ALL_LABELS.filter((label) => screen.queryByRole('button', { name: label }) !== null);

describe('QuoteStatusActions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('offers only the transitions valid from the current status', () => {
    const draft = render(<QuoteStatusActions quote={createMockQuote({ status: 'draft' })} />);
    expect(offeredLabels()).toEqual(['Mark as sent']);
    draft.unmount();

    const sent = render(<QuoteStatusActions quote={createMockQuote({ status: 'sent' })} />);
    expect(offeredLabels()).toEqual(['Accept', 'Reject', 'Mark expired', 'Revise']);
    sent.unmount();

    for (const status of ['accepted', 'rejected', 'expired'] as const) {
      const closed = render(<QuoteStatusActions quote={createMockQuote({ status })} />);
      expect(offeredLabels()).toEqual([]);
      expect(screen.getByText('No further status changes')).toBeInTheDocument();
      closed.unmount();
    }
  });

  it('posts the target status and hands the returned quote to the caller', async () => {
    const onChanged = vi.fn();
    const moved = createMockQuote({ status: 'sent', sent_at: '2026-10-27T10:00:00Z' });
    vi.mocked(quotesApi.changeStatus).mockResolvedValue(moved);
    render(<QuoteStatusActions quote={createMockQuote({ id: 42, status: 'draft' })} onChanged={onChanged} />);

    fireEvent.click(screen.getByRole('button', { name: 'Mark as sent' }));

    await waitFor(() => expect(quotesApi.changeStatus).toHaveBeenCalledWith(42, 'sent'));
    await waitFor(() => expect(onChanged).toHaveBeenCalledWith(moved));
    expect(showSuccess).toHaveBeenCalledWith('Quote status updated');
    expect(screen.queryByTestId('quote-status-error')).not.toBeInTheDocument();
  });

  it('shows the server message for INVALID_STATUS_TRANSITION and CONFLICT responses', async () => {
    vi.mocked(quotesApi.changeStatus).mockRejectedValueOnce(
      apiError(400, 'INVALID_STATUS_TRANSITION', 'invalid quote status transition')
    );
    render(<QuoteStatusActions quote={createMockQuote({ id: 42, status: 'draft' })} />);

    fireEvent.click(screen.getByRole('button', { name: 'Mark as sent' }));

    expect(await screen.findByTestId('quote-status-error')).toHaveTextContent('invalid quote status transition');
    expect(showError).toHaveBeenCalledWith('invalid quote status transition');

    vi.mocked(quotesApi.changeStatus).mockRejectedValueOnce(apiError(409, 'CONFLICT', 'quote has no lines'));
    fireEvent.click(screen.getByRole('button', { name: 'Mark as sent' }));

    await waitFor(() => expect(screen.getByTestId('quote-status-error')).toHaveTextContent('quote has no lines'));
    expect(showError).toHaveBeenLastCalledWith('quote has no lines');
    expect(quotesApi.changeStatus).toHaveBeenCalledTimes(2);
  });
});
