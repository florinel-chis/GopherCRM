import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { QuoteEmailDialog } from './QuoteEmailDialog';
import { quotesApi } from '@/api/endpoints';
import { createMockQuote } from '@/test/factories';

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    emailQuote: vi.fn(),
  },
}));

const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

describe('QuoteEmailDialog', () => {
  const quote = createMockQuote({
    id: 42,
    status: 'sent',
    customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
  });

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('names the customer recipient and shows the last emailed time on success', async () => {
    const onEmailed = vi.fn();
    const emailed = { ...quote, last_emailed_at: '2026-10-27T12:00:00Z' };
    vi.mocked(quotesApi.emailQuote).mockResolvedValue(emailed);
    render(<QuoteEmailDialog open quote={quote} onClose={vi.fn()} onEmailed={onEmailed} />);

    const dialog = await screen.findByRole('dialog', { name: 'Email quote Q-000042' });
    expect(screen.getByTestId('quote-email-recipient')).toHaveTextContent('Jane Smith (Acme Widgets)');
    // The address is never shown or editable.
    expect(dialog.querySelector('input')).toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Send email' }));

    await waitFor(() => expect(quotesApi.emailQuote).toHaveBeenCalledWith(42));
    expect(await screen.findByTestId('quote-email-success')).toHaveTextContent(/Emailed on Oct 27, 2026/);
    expect(onEmailed).toHaveBeenCalledWith(emailed);
    expect(screen.queryByRole('button', { name: 'Send email' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
  });

  it('shows distinct messages for 409 and 502 responses', async () => {
    vi.mocked(quotesApi.emailQuote).mockRejectedValueOnce(
      apiError(409, 'CONFLICT', 'quote was revised while the email was being sent')
    );
    const first = render(<QuoteEmailDialog open quote={quote} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Send email' }));
    const conflict = await screen.findByTestId('quote-email-error');
    expect(conflict).toHaveTextContent(
      'The quote was not emailed: quote was revised while the email was being sent.'
    );
    // Not stamped: the send button stays for a retry.
    expect(screen.getByRole('button', { name: 'Send email' })).toBeInTheDocument();
    first.unmount();

    vi.mocked(quotesApi.emailQuote).mockRejectedValueOnce(
      apiError(502, 'MAIL_DELIVERY_FAILED', 'The quote email could not be delivered')
    );
    render(<QuoteEmailDialog open quote={quote} onClose={vi.fn()} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Send email' }));
    const delivery = await screen.findByTestId('quote-email-error');
    expect(delivery).toHaveTextContent(
      'The quote email could not be delivered. The quote was not changed; try again later.'
    );
    expect(delivery).not.toHaveTextContent('was not emailed');
  });

  it('refuses to send when the quote has no customer', async () => {
    render(<QuoteEmailDialog open quote={createMockQuote({ id: 42, status: 'sent' })} onClose={vi.fn()} />);

    expect(await screen.findByTestId('quote-email-no-recipient')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send email' })).toBeDisabled();
  });
});
