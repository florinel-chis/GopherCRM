import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@/test/test-utils';
import { Component as QuotePrint } from './QuotePrint';
import { quotesApi } from '@/api/endpoints';
import { createMockQuote, createMockQuoteLine } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';
import { formatMoney } from './quoteFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    getQuote: vi.fn(),
  },
}));

describe('QuotePrint', () => {
  const quote = createMockQuote({
    id: 42,
    number: 'Q-000042',
    title: 'Website redesign quote',
    status: 'sent',
    currency: 'EUR',
    subtotal_cents: 2500,
    discount_cents: 250,
    total_cents: 2250,
    valid_until: '2099-12-31',
    notes: 'Payment within 30 days',
    sent_at: '2026-10-27T10:00:00Z',
    customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
    lines: [
      createMockQuoteLine({ id: 7, description: 'Widget', quantity: 2, unit_price_cents: 1250, discount_bp: 1000, line_total_cents: 2250 }),
    ],
  });
  const originalPrint = window.print;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '42' });
    vi.mocked(quotesApi.getQuote).mockResolvedValue(quote);
  });

  afterEach(() => {
    Object.defineProperty(window, 'print', { value: originalPrint, configurable: true, writable: true });
  });

  it('renders the header, the lines and the totals from the quote', async () => {
    render(<QuotePrint />);

    expect(await screen.findByTestId('quote-print-number')).toHaveTextContent('Quote Q-000042');
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(/GoCRM|GopherCRM/);
    expect(screen.getByTestId('quote-print-title')).toHaveTextContent('Website redesign quote');
    expect(screen.getByTestId('quote-print-date')).toHaveTextContent('Oct 27, 2026');
    expect(screen.getByTestId('quote-print-valid-until')).toHaveTextContent('Dec 31, 2099');
    expect(screen.getByTestId('quote-print-customer')).toHaveTextContent('Jane Smith');
    expect(screen.getByTestId('quote-print-customer')).toHaveTextContent('Acme Widgets');

    const lines = screen.getAllByTestId('quote-print-line');
    expect(lines).toHaveLength(1);
    expect(lines[0]).toHaveTextContent('Widget');
    expect(lines[0]).toHaveTextContent('2');
    expect(lines[0]).toHaveTextContent(formatMoney(1250, 'EUR'));
    expect(lines[0]).toHaveTextContent('10%');
    expect(lines[0]).toHaveTextContent(formatMoney(2250, 'EUR'));

    expect(screen.getByTestId('quote-print-subtotal')).toHaveTextContent(formatMoney(2500, 'EUR'));
    expect(screen.getByTestId('quote-print-discount')).toHaveTextContent(formatMoney(250, 'EUR'));
    expect(screen.getByTestId('quote-print-total')).toHaveTextContent(formatMoney(2250, 'EUR'));
    expect(screen.getByTestId('quote-print-notes')).toHaveTextContent('Payment within 30 days');
  });

  it('calls window.print when Print is clicked', async () => {
    const print = vi.fn();
    Object.defineProperty(window, 'print', { value: print, configurable: true, writable: true });
    render(<QuotePrint />);

    fireEvent.click(await screen.findByRole('button', { name: 'Print' }));

    expect(print).toHaveBeenCalledTimes(1);
  });

  it('says so when the quote cannot be loaded', async () => {
    vi.mocked(quotesApi.getQuote).mockRejectedValue(new Error('not found'));
    render(<QuotePrint />);

    expect(await screen.findByText('Quote not found')).toBeInTheDocument();
  });
});
