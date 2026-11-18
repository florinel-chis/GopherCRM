import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { parseISO } from 'date-fns';
import { formatDate } from '@/utils/date';
import { formatMoney } from './quoteFormat';
import { QuoteResponse } from './QuoteResponse';

const mockGetQuoteResponse = vi.fn();
const mockRespond = vi.fn();

vi.mock('@/api/endpoints', () => ({
  quoteResponseApi: {
    getQuoteResponse: (token: string) => mockGetQuoteResponse(token),
    respond: (token: string, decision: string) => mockRespond(token, decision),
  },
}));

const INVALID = 'This link is no longer valid. It may already have been used or it has expired.';
const UNAVAILABLE = 'The quote could not be loaded. Please try again later.';

// Distinct amounts in a non-default currency, so a swapped field or a
// hard-coded currency fails.
const quote = {
  number: 'Q-000042',
  status: 'sent',
  currency: 'USD',
  valid_until: '2026-12-31',
  lines: [
    {
      description: 'Analytical engine licence',
      quantity: 1.5,
      unit_price_cents: 20000,
      discount_bp: 1250,
      line_total_cents: 26250,
      tax_rate_bp: 1900,
      tax_cents: 4988,
    },
  ],
  subtotal_cents: 30000,
  discount_cents: 3750,
  total_cents: 26250,
  tax_cents: 4988,
  gross_total_cents: 31238,
};

const rejection = (status: number, code?: string) => ({
  response: { status, data: code ? { code, message: 'Invalid or expired quote link' } : {} },
});

const renderPage = (url = '/quote-response?token=t') =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <QuoteResponse />
    </MemoryRouter>
  );

const expectNoQuote = () => {
  expect(screen.queryByText(/Q-000042/)).toBeNull();
  expect(screen.queryByText('Gross')).toBeNull();
  expect(screen.queryByText('Analytical engine licence')).toBeNull();
  expect(screen.queryByRole('button', { name: /accept|decline/i })).toBeNull();
};

describe('QuoteResponse page', () => {
  beforeEach(() => {
    mockGetQuoteResponse.mockReset();
    mockRespond.mockReset();
    mockGetQuoteResponse.mockResolvedValue(quote);
    mockRespond.mockResolvedValue({ ...quote, status: 'accepted' });
  });

  it('shows the quote number, the validity date, the line and the totals with the currency', async () => {
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Quote Q-000042' })).toBeInTheDocument();
    expect(mockGetQuoteResponse).toHaveBeenCalledTimes(1);
    expect(mockGetQuoteResponse).toHaveBeenCalledWith('t');

    const validUntil = formatDate(parseISO(quote.valid_until), 'MMM dd, yyyy');
    expect(validUntil).toBe('Dec 31, 2026');
    expect(screen.getByTestId('quote-response-valid-until')).toHaveTextContent(validUntil);
    expect(screen.getByText(/Valid until/)).toBeInTheDocument();

    const line = within(screen.getByTestId('quote-response-line'));
    expect(line.getByText('Analytical engine licence')).toBeInTheDocument();
    expect(line.getByText('1.5')).toBeInTheDocument();
    expect(line.getByText(formatMoney(20000, 'USD'))).toBeInTheDocument();
    expect(line.getByText('12.5%')).toBeInTheDocument();
    expect(line.getByText('19%')).toBeInTheDocument();
    expect(line.getByText(formatMoney(26250, 'USD'))).toBeInTheDocument();

    for (const label of ['Subtotal', 'Net', 'Tax', 'Gross']) {
      expect(screen.getByRole('rowheader', { name: label })).toBeInTheDocument();
    }
    expect(screen.getByTestId('quote-response-subtotal')).toHaveTextContent(formatMoney(30000, 'USD'));
    expect(screen.getByTestId('quote-response-discount')).toHaveTextContent(formatMoney(3750, 'USD'));
    expect(screen.getByTestId('quote-response-net')).toHaveTextContent(formatMoney(26250, 'USD'));
    expect(screen.getByTestId('quote-response-tax')).toHaveTextContent(formatMoney(4988, 'USD'));
    expect(screen.getByTestId('quote-response-gross')).toHaveTextContent(formatMoney(31238, 'USD'));
    // The currency is the quote's, not a default.
    expect(formatMoney(31238, 'USD')).not.toBe(formatMoney(31238, 'EUR'));

    expect(screen.getByRole('button', { name: 'Accept' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Decline' })).toBeInTheDocument();
    // A public page: no way into the application from here.
    expect(screen.queryByRole('link')).toBeNull();
    // The token is never rendered.
    expect(document.body.textContent).not.toContain('token');
  });

  it('omits the validity row when the quote has no validity date', async () => {
    mockGetQuoteResponse.mockResolvedValue({ ...quote, valid_until: null });
    renderPage();

    expect(await screen.findByRole('heading', { name: 'Quote Q-000042' })).toBeInTheDocument();
    expect(screen.queryByText(/Valid until/)).toBeNull();
    expect(screen.queryByTestId('quote-response-valid-until')).toBeNull();
  });

  it('sends nothing when Accept is clicked without Confirm, and Cancel closes the dialog', async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Accept' }));
    const dialog = await screen.findByRole('dialog', { name: 'Accept quote Q-000042?' });
    expect(mockRespond).not.toHaveBeenCalled();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(mockRespond).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Accept' })).toBeInTheDocument();
  });

  it('accepts after Confirm and then shows only the confirmation', async () => {
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Accept' }));
    const dialog = await screen.findByRole('dialog', { name: 'Accept quote Q-000042?' });
    await user.click(within(dialog).getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('You have accepted quote Q-000042.')).toBeInTheDocument();
    expect(mockRespond).toHaveBeenCalledTimes(1);
    expect(mockRespond).toHaveBeenCalledWith('t', 'accept');
    expect(screen.queryByRole('button', { name: /accept|decline/i })).toBeNull();
    expect(screen.queryByText('Gross')).toBeNull();
    expect(mockGetQuoteResponse).toHaveBeenCalledTimes(1);
  });

  it('declines after Confirm and then shows only the confirmation', async () => {
    mockRespond.mockResolvedValue({ ...quote, status: 'rejected' });
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Decline' }));
    const dialog = await screen.findByRole('dialog', { name: 'Decline quote Q-000042?' });
    await user.click(within(dialog).getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText('You have declined quote Q-000042.')).toBeInTheDocument();
    expect(mockRespond).toHaveBeenCalledTimes(1);
    expect(mockRespond).toHaveBeenCalledWith('t', 'decline');
    expect(screen.queryByRole('button', { name: /accept|decline/i })).toBeNull();
  });

  it('sends one decision when Confirm is clicked twice while the first is in flight', async () => {
    let resolve: (value: unknown) => void = () => {};
    mockRespond.mockReturnValue(new Promise((r) => (resolve = r)));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Accept' }));
    const dialog = await screen.findByRole('dialog', { name: 'Accept quote Q-000042?' });
    const confirm = within(dialog).getByRole('button', { name: 'Confirm' });
    // Two clicks before any re-render, then one more once the buttons are
    // disabled.
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    await waitFor(() => expect(confirm).toBeDisabled());
    fireEvent.click(confirm);
    expect(within(dialog).getByRole('button', { name: 'Cancel' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Accept', hidden: true })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Decline', hidden: true })).toBeDisabled();
    expect(mockRespond).toHaveBeenCalledTimes(1);

    resolve({ ...quote, status: 'accepted' });

    expect(await screen.findByText('You have accepted quote Q-000042.')).toBeInTheDocument();
    expect(mockRespond).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(INVALID)).toBeNull();
  });

  it('shows only the invalid-link message when the read is rejected with 400', async () => {
    mockGetQuoteResponse.mockRejectedValue(rejection(400, 'BAD_REQUEST'));
    renderPage();

    expect(await screen.findByText(INVALID)).toBeInTheDocument();
    expectNoQuote();
    expect(mockGetQuoteResponse).toHaveBeenCalledTimes(1);
  });

  it('shows the invalid-link message without a request when the token is missing or empty', async () => {
    for (const url of ['/quote-response', '/quote-response?token=']) {
      const view = renderPage(url);
      expect(screen.getByText(INVALID)).toBeInTheDocument();
      expectNoQuote();
      view.unmount();
    }
    expect(mockGetQuoteResponse).not.toHaveBeenCalled();
  });

  it('switches to the invalid-link message when the decision is rejected with 400 BAD_REQUEST', async () => {
    mockRespond.mockRejectedValue(rejection(400, 'BAD_REQUEST'));
    const user = userEvent.setup();
    renderPage();

    await user.click(await screen.findByRole('button', { name: 'Accept' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Confirm' }));

    expect(await screen.findByText(INVALID)).toBeInTheDocument();
    expectNoQuote();
    expect(screen.queryByText(/You have accepted/)).toBeNull();
  });

  it('shows only the load-failure message when the read fails otherwise', async () => {
    for (const failure of [rejection(500), rejection(429), new Error('Network Error')]) {
      mockGetQuoteResponse.mockRejectedValue(failure);
      const view = renderPage();

      expect(await screen.findByText(UNAVAILABLE)).toBeInTheDocument();
      expect(screen.queryByText(INVALID)).toBeNull();
      expectNoQuote();
      view.unmount();
    }
    // One read per mount: no retry.
    expect(mockGetQuoteResponse).toHaveBeenCalledTimes(3);
  });

  it('keeps the quote and both buttons for a retry when the decision fails otherwise', async () => {
    const user = userEvent.setup();
    for (const failure of [rejection(400, 'VALIDATION_ERROR'), rejection(500), rejection(429)]) {
      mockRespond.mockReset();
      mockRespond.mockRejectedValueOnce(failure);
      mockRespond.mockResolvedValue({ ...quote, status: 'rejected' });
      const view = renderPage();

      await user.click(await screen.findByRole('button', { name: 'Decline' }));
      await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Confirm' }));

      expect(await screen.findByRole('alert')).toHaveTextContent(
        'Your answer could not be recorded. Please try again.'
      );
      expect(screen.queryByText(INVALID)).toBeNull();
      expect(screen.getByRole('heading', { name: 'Quote Q-000042' })).toBeInTheDocument();
      expect(screen.getByTestId('quote-response-gross')).toHaveTextContent(formatMoney(31238, 'USD'));
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(screen.getByRole('button', { name: 'Accept' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Decline' })).toBeEnabled();

      // The retry goes through.
      await user.click(screen.getByRole('button', { name: 'Decline' }));
      await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Confirm' }));
      expect(await screen.findByText('You have declined quote Q-000042.')).toBeInTheDocument();
      expect(mockRespond).toHaveBeenCalledTimes(2);
      view.unmount();
    }
  });
});
