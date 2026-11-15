import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import { Component as QuoteDetail } from './QuoteDetail';
import { quotesApi } from '@/api/endpoints';
import { createMockQuote, createMockQuoteLine, createMockUser } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';
import type { Quote, User } from '@/types';
import { formatMoney } from './quoteFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    getQuote: vi.fn(),
    getHistory: vi.fn(),
    addLine: vi.fn(),
    updateLine: vi.fn(),
    deleteLine: vi.fn(),
    changeStatus: vi.fn(),
    emailQuote: vi.fn(),
    updateQuote: vi.fn(),
    deleteQuote: vi.fn(),
  },
  productsApi: {
    getProducts: vi.fn(),
    getProduct: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const headerTexts = (): string[] => {
  const table = screen.getByRole('table', { name: 'Quote lines' });
  const header = within(table).getAllByRole('row')[0];
  return within(header)
    .getAllByRole('columnheader')
    .map((cell) => cell.textContent ?? '');
};

const totalsRowLabel = (testId: string): HTMLElement => {
  const row = screen.getByTestId(testId).closest('tr') as HTMLElement;
  return within(row).getAllByRole('cell')[0];
};

describe('QuoteDetail tax', () => {
  const lines = [
    createMockQuoteLine({
      id: 7,
      position: 1,
      description: 'Widget',
      quantity: 2,
      unit_price_cents: 1250,
      discount_bp: 1000,
      line_total_cents: 2250,
      tax_rate_bp: 2100,
      tax_cents: 473,
    }),
    createMockQuoteLine({
      id: 8,
      position: 2,
      description: 'Book',
      quantity: 1,
      unit_price_cents: 1000,
      discount_bp: 0,
      line_total_cents: 1000,
      tax_rate_bp: 550,
      tax_cents: 55,
    }),
  ];
  // Deliberately inconsistent: gross is not total + tax and tax is not the
  // sum of the lines, so only the server values can produce these texts.
  const taxed: Quote = createMockQuote({
    id: 42,
    status: 'draft',
    currency: 'EUR',
    subtotal_cents: 3500,
    discount_cents: 250,
    total_cents: 3250,
    tax_cents: 529,
    gross_total_cents: 3790,
    lines,
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '42' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(quotesApi.getQuote).mockResolvedValue(taxed);
    vi.mocked(quotesApi.getHistory).mockResolvedValue([]);
  });

  it('shows each line tax rate and tax amount from the server response', async () => {
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    expect(headerTexts()).toEqual([
      'Description',
      'Qty',
      'Unit price',
      'Discount',
      'Tax rate',
      'Tax',
      'Line total',
      'Actions',
    ]);

    const rows = screen.getAllByTestId('quote-line-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByTestId('quote-line-tax-rate')).toHaveTextContent('21%');
    expect(within(rows[0]).getByTestId('quote-line-tax')).toHaveTextContent(formatMoney(473, 'EUR'));
    expect(within(rows[1]).getByTestId('quote-line-tax-rate')).toHaveTextContent('5.5%');
    expect(within(rows[1]).getByTestId('quote-line-tax')).toHaveTextContent(formatMoney(55, 'EUR'));
  });

  it('shows net total, tax and gross total from the server values without recomputing them', async () => {
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    expect(screen.getByTestId('quote-subtotal')).toHaveTextContent(formatMoney(3500, 'EUR'));
    expect(screen.getByTestId('quote-discount')).toHaveTextContent(formatMoney(250, 'EUR'));
    expect(screen.getByTestId('quote-total')).toHaveTextContent(formatMoney(3250, 'EUR'));
    expect(screen.getByTestId('quote-tax')).toHaveTextContent(formatMoney(529, 'EUR'));
    // 3250 + 529 would be 3779; the server said 3790.
    expect(screen.getByTestId('quote-gross')).toHaveTextContent(formatMoney(3790, 'EUR'));
    expect(screen.getByTestId('quote-gross')).not.toHaveTextContent(formatMoney(3779, 'EUR'));

    expect(totalsRowLabel('quote-subtotal')).toHaveTextContent('Subtotal');
    expect(totalsRowLabel('quote-discount')).toHaveTextContent('Discount');
    expect(totalsRowLabel('quote-total')).toHaveTextContent('Net total');
    expect(totalsRowLabel('quote-tax')).toHaveTextContent(/^Tax$/);
    expect(totalsRowLabel('quote-gross')).toHaveTextContent('Gross total');
  });

  it('shows the tax columns and totals on a quote that is not a draft', async () => {
    vi.mocked(quotesApi.getQuote).mockResolvedValue({ ...taxed, status: 'sent', sent_at: '2026-10-27T10:00:00Z' });
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    expect(screen.queryByRole('button', { name: 'Add line' })).not.toBeInTheDocument();
    expect(headerTexts()).toEqual(['Description', 'Qty', 'Unit price', 'Discount', 'Tax rate', 'Tax', 'Line total']);
    const rows = screen.getAllByTestId('quote-line-row');
    expect(within(rows[0]).getByTestId('quote-line-tax-rate')).toHaveTextContent('21%');
    expect(within(rows[0]).getByTestId('quote-line-tax')).toHaveTextContent(formatMoney(473, 'EUR'));
    expect(screen.getByTestId('quote-total')).toHaveTextContent(formatMoney(3250, 'EUR'));
    expect(screen.getByTestId('quote-tax')).toHaveTextContent(formatMoney(529, 'EUR'));
    expect(screen.getByTestId('quote-gross')).toHaveTextContent(formatMoney(3790, 'EUR'));
  });

  it('shows zero tax and a gross equal to the net total for a quote without tax', async () => {
    const untaxed = createMockQuote({
      id: 42,
      status: 'sent',
      currency: 'USD',
      subtotal_cents: 2500,
      discount_cents: 250,
      total_cents: 2250,
      tax_cents: 0,
      gross_total_cents: 2250,
      lines: [
        createMockQuoteLine({ id: 7, discount_bp: 1000, line_total_cents: 2250, tax_rate_bp: 0, tax_cents: 0 }),
      ],
    });
    vi.mocked(quotesApi.getQuote).mockResolvedValue(untaxed);
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    const [row] = screen.getAllByTestId('quote-line-row');
    expect(within(row).getByTestId('quote-line-tax-rate')).toHaveTextContent(/^0%$/);
    expect(within(row).getByTestId('quote-line-tax')).toHaveTextContent(formatMoney(0, 'USD'));
    expect(screen.getByTestId('quote-tax')).toHaveTextContent(formatMoney(0, 'USD'));
    expect(screen.getByTestId('quote-gross').textContent).toBe(screen.getByTestId('quote-total').textContent);
    expect(screen.getByTestId('quote-gross')).toHaveTextContent(formatMoney(2250, 'USD'));
  });
});
