import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
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

const totalsRowLabel = (testId: string): HTMLElement => {
  const row = screen.getByTestId(testId).closest('tr') as HTMLElement;
  return within(row).getAllByRole('cell')[0];
};

describe('QuotePrint tax', () => {
  // Deliberately inconsistent totals: only the server values produce these texts.
  const quote = createMockQuote({
    id: 42,
    number: 'Q-000042',
    status: 'sent',
    currency: 'EUR',
    subtotal_cents: 2500,
    discount_cents: 250,
    total_cents: 2250,
    tax_cents: 473,
    gross_total_cents: 2730,
    sent_at: '2026-10-27T10:00:00Z',
    lines: [
      createMockQuoteLine({
        id: 7,
        description: 'Widget',
        quantity: 2,
        unit_price_cents: 1250,
        discount_bp: 1000,
        line_total_cents: 2250,
        tax_rate_bp: 2100,
        tax_cents: 473,
      }),
    ],
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '42' });
    vi.mocked(quotesApi.getQuote).mockResolvedValue(quote);
  });

  it('prints each line tax rate and tax amount', async () => {
    render(<QuotePrint />);
    await screen.findByTestId('quote-print-number');

    const table = screen.getByRole('table', { name: 'Quote lines' });
    const header = within(table).getAllByRole('row')[0];
    expect(within(header).getAllByRole('columnheader').map((cell) => cell.textContent)).toEqual([
      'Description',
      'Qty',
      'Unit price',
      'Discount',
      'Tax rate',
      'Tax',
      'Line total',
    ]);

    const [line] = screen.getAllByTestId('quote-print-line');
    expect(within(line).getByTestId('quote-print-line-tax-rate')).toHaveTextContent('21%');
    expect(within(line).getByTestId('quote-print-line-tax')).toHaveTextContent(formatMoney(473, 'EUR'));
  });

  it('prints net total, tax and gross total from the server values', async () => {
    render(<QuotePrint />);
    await screen.findByTestId('quote-print-number');

    expect(screen.getByTestId('quote-print-total')).toHaveTextContent(formatMoney(2250, 'EUR'));
    expect(screen.getByTestId('quote-print-tax')).toHaveTextContent(formatMoney(473, 'EUR'));
    // 2250 + 473 would be 2723; the server said 2730.
    expect(screen.getByTestId('quote-print-gross')).toHaveTextContent(formatMoney(2730, 'EUR'));
    expect(screen.getByTestId('quote-print-gross')).not.toHaveTextContent(formatMoney(2723, 'EUR'));

    expect(totalsRowLabel('quote-print-total')).toHaveTextContent('Net total');
    expect(totalsRowLabel('quote-print-tax')).toHaveTextContent(/^Tax$/);
    expect(totalsRowLabel('quote-print-gross')).toHaveTextContent('Gross total');
  });

  it('spans the empty lines row across every column', async () => {
    vi.mocked(quotesApi.getQuote).mockResolvedValue({ ...quote, lines: [] });
    render(<QuotePrint />);
    await screen.findByTestId('quote-print-number');

    const empty = screen.getByText('No lines').closest('td') as HTMLElement;
    expect(empty).toHaveAttribute('colspan', '7');
    const table = screen.getByRole('table', { name: 'Quote lines' });
    expect(within(table).getAllByRole('columnheader')).toHaveLength(7);
    expect(screen.queryAllByTestId('quote-print-line')).toHaveLength(0);
  });
});
