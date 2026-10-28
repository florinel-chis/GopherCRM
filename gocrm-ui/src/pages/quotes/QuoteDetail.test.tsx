import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as QuoteDetail } from './QuoteDetail';
import { productsApi, quotesApi } from '@/api/endpoints';
import { createMockProduct, createMockQuote, createMockQuoteLine, createMockQuoteStatusChange, createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
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

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
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

const EDIT_CONTROLS = ['Edit', 'Add line', 'Edit line 1', 'Delete line 1', 'Edit line 2', 'Delete line 2'];

describe('QuoteDetail', () => {
  const mockNavigate = vi.fn();
  const lines = [
    createMockQuoteLine({ id: 7, position: 1, description: 'Widget', quantity: 2, unit_price_cents: 1250, discount_bp: 1000, line_total_cents: 2250 }),
    createMockQuoteLine({ id: 8, position: 2, description: 'Gadget', quantity: 1, unit_price_cents: 99900, discount_bp: 0, line_total_cents: 99900 }),
  ];
  const draft: Quote = createMockQuote({
    id: 42,
    number: 'Q-000042',
    title: 'Website redesign quote',
    status: 'draft',
    currency: 'EUR',
    subtotal_cents: 102400,
    discount_cents: 250,
    total_cents: 102150,
    valid_until: '2099-12-31',
    customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
    deal: { id: 9, title: 'Website redesign', stage: 'proposal' },
    owner: { id: 1, first_name: 'Ana', last_name: 'Pop' },
    lines,
  });
  const history = [
    createMockQuoteStatusChange({ id: 1, from_status: null, to_status: 'draft' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '42' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(quotesApi.getQuote).mockResolvedValue(draft);
    vi.mocked(quotesApi.getHistory).mockResolvedValue(history);
    vi.mocked(productsApi.getProducts).mockResolvedValue({
      products: [createMockProduct({ id: 1, name: 'Widget', sku: 'SKU-001', unit_price_cents: 1250, currency: 'EUR' })],
      total: 1,
    });
  });

  it('shows the lines and the subtotal, discount and total from the server response', async () => {
    render(<QuoteDetail />);

    expect(await screen.findByTestId('quote-number')).toHaveTextContent('Q-000042');
    expect(screen.getByTestId('quote-title')).toHaveTextContent('Website redesign quote');
    expect(screen.getByTestId('quote-status-chip')).toHaveTextContent('Draft');
    expect(screen.getByTestId('quote-customer')).toHaveTextContent('Jane Smith (Acme Widgets)');
    expect(screen.getByRole('link', { name: 'Website redesign' })).toHaveAttribute('href', '/deals/9');
    expect(screen.getByText('Ana Pop')).toBeInTheDocument();
    expect(screen.getByTestId('quote-valid-until')).toHaveTextContent('Dec 31, 2099');

    const rows = screen.getAllByTestId('quote-line-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText('Widget')).toBeInTheDocument();
    expect(within(rows[0]).getByText('2')).toBeInTheDocument();
    expect(within(rows[0]).getByText(formatMoney(1250, 'EUR'))).toBeInTheDocument();
    expect(within(rows[0]).getByText('10%')).toBeInTheDocument();
    expect(within(rows[0]).getByText(formatMoney(2250, 'EUR'))).toBeInTheDocument();
    expect(within(rows[1]).getByText('Gadget')).toBeInTheDocument();
    expect(within(rows[1]).getByText('0%')).toBeInTheDocument();

    // The totals are the server's, not a client-side sum.
    expect(screen.getByTestId('quote-subtotal')).toHaveTextContent(formatMoney(102400, 'EUR'));
    expect(screen.getByTestId('quote-discount')).toHaveTextContent(formatMoney(250, 'EUR'));
    expect(screen.getByTestId('quote-total')).toHaveTextContent(formatMoney(102150, 'EUR'));

    const historyRows = within(await screen.findByRole('list', { name: 'Status history' })).getAllByTestId('quote-history-row');
    expect(historyRows).toHaveLength(1);
    expect(historyRows[0]).toHaveTextContent('Created as Draft');
  });

  it('offers Add line on a draft and submits discount_bp 1250 for 12.5 percent', async () => {
    const withLine = { ...draft, lines: [...lines, createMockQuoteLine({ id: 9, position: 3, description: 'Widget' })] };
    vi.mocked(quotesApi.addLine).mockResolvedValue(withLine);
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add line' });

    await pickAutocompleteOption('Product', 'Widget (SKU-001)');
    await waitFor(() =>
      expect(productsApi.getProducts).toHaveBeenCalledWith(
        expect.objectContaining({ is_active: true, currency: 'EUR' })
      )
    );
    fireEvent.change(within(dialog).getByLabelText(/^Quantity/), { target: { value: '2' } });
    fireEvent.change(within(dialog).getByLabelText(/^Discount/), { target: { value: '12.5' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

    await waitFor(() => expect(quotesApi.addLine).toHaveBeenCalledWith(42, { product_id: 1, quantity: 2, discount_bp: 1250 }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Add line' })).not.toBeInTheDocument());
    expect(screen.getAllByTestId('quote-line-row')).toHaveLength(3);
    expect(showSuccess).toHaveBeenCalledWith('Line added');
  });

  it('refuses an invalid discount and quantity locally', async () => {
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Add line' }));
    const dialog = await screen.findByRole('dialog', { name: 'Add line' });
    fireEvent.change(within(dialog).getByLabelText(/^Quantity/), { target: { value: '0' } });
    fireEvent.change(within(dialog).getByLabelText(/^Discount/), { target: { value: '101' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

    expect(await within(dialog).findByText('Pick a product')).toBeInTheDocument();
    expect(within(dialog).getByText('Quantity must be a whole number between 1 and 1,000,000')).toBeInTheDocument();
    expect(
      within(dialog).getByText('Discount must be a percentage between 0 and 100 with at most two decimals')
    ).toBeInTheDocument();
    expect(quotesApi.addLine).not.toHaveBeenCalled();
  });

  it('edits a line with the discount sent in basis points and removes one after confirming', async () => {
    vi.mocked(quotesApi.updateLine).mockResolvedValue(draft);
    vi.mocked(quotesApi.deleteLine).mockResolvedValue({ ...draft, lines: [lines[1]] });
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Edit line 1' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit line' });
    expect(within(dialog).getByLabelText(/^Description/)).toHaveValue('Widget');
    expect(within(dialog).getByLabelText(/^Discount/)).toHaveValue('10');
    expect(within(dialog).getByLabelText(/^Unit price/)).toHaveValue('12.50');
    fireEvent.change(within(dialog).getByLabelText(/^Quantity/), { target: { value: '3' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(quotesApi.updateLine).toHaveBeenCalledWith(42, 7, {
        description: 'Widget',
        quantity: 3,
        discount_bp: 1000,
        unit_price_cents: 1250,
      })
    );
    // The dialog closes once the mutation resolves; the page behind it is aria-hidden until then.
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Edit line' })).not.toBeInTheDocument());
    expect(showSuccess).toHaveBeenCalledWith('Line updated');

    fireEvent.click(screen.getByRole('button', { name: 'Delete line 1' }));
    const confirm = await screen.findByRole('dialog', { name: 'Remove line' });
    fireEvent.click(within(confirm).getByRole('button', { name: 'Remove' }));

    await waitFor(() => expect(quotesApi.deleteLine).toHaveBeenCalledWith(42, 7));
    await waitFor(() => expect(screen.getAllByTestId('quote-line-row')).toHaveLength(1));
  });

  it('hides every edit control when the quote is not a draft', async () => {
    for (const status of ['sent', 'accepted', 'rejected', 'expired'] as const) {
      vi.mocked(quotesApi.getQuote).mockResolvedValue({ ...draft, status, sent_at: '2026-10-27T10:00:00Z' });
      const view = render(<QuoteDetail />);
      await screen.findByTestId('quote-number');
      expect(screen.getByTestId('quote-status-chip')).toHaveAttribute('data-status', status);

      for (const label of EDIT_CONTROLS) {
        expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument();
      }
      // The lines and the totals still read.
      expect(screen.getAllByTestId('quote-line-row')).toHaveLength(2);
      expect(screen.getByTestId('quote-total')).toHaveTextContent(formatMoney(102150, 'EUR'));
      view.unmount();
    }
  });

  it('marks a draft as sent, stores the returned quote and refreshes the history', async () => {
    const sent = { ...draft, status: 'sent' as const, sent_at: '2026-10-27T10:00:00Z' };
    vi.mocked(quotesApi.changeStatus).mockResolvedValue(sent);
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Mark as sent' }));

    await waitFor(() => expect(quotesApi.changeStatus).toHaveBeenCalledWith(42, 'sent'));
    await waitFor(() => expect(screen.getByTestId('quote-status-chip')).toHaveTextContent('Sent'));
    await waitFor(() => expect(quotesApi.getHistory).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('button', { name: 'Add line' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Email quote' })).toBeInTheDocument();
  });

  it('offers Email quote on sent and accepted quotes only, with the Print link always', async () => {
    const { unmount } = render(<QuoteDetail />);
    await screen.findByTestId('quote-number');
    expect(screen.queryByRole('button', { name: 'Email quote' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Print' })).toHaveAttribute('href', '/quotes/42/print');
    unmount();

    vi.mocked(quotesApi.getQuote).mockResolvedValue({ ...draft, status: 'accepted' });
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Email quote' }));
    expect(await screen.findByRole('dialog', { name: 'Email quote Q-000042' })).toBeInTheDocument();
  });

  it('edits the title, validity and notes of a draft', async () => {
    vi.mocked(quotesApi.updateQuote).mockResolvedValue({ ...draft, title: 'Renamed', notes: 'Net 30' });
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog', { name: 'Edit quote' });
    fireEvent.change(within(dialog).getByLabelText(/^Title/), { target: { value: 'Renamed' } });
    fireEvent.change(within(dialog).getByLabelText(/^Valid until/), { target: { value: '' } });
    fireEvent.change(within(dialog).getByLabelText(/^Notes/), { target: { value: 'Net 30' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(quotesApi.updateQuote).toHaveBeenCalledWith(42, { title: 'Renamed', valid_until: null, notes: 'Net 30' })
    );
    await waitFor(() => expect(screen.getByTestId('quote-title')).toHaveTextContent('Renamed'));
  });

  it('lets an admin delete and returns to the list', async () => {
    vi.mocked(quotesApi.deleteQuote).mockResolvedValue();
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    fireEvent.click(screen.getByRole('button', { name: 'Delete quote' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Quote' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(quotesApi.deleteQuote).toHaveBeenCalledWith(42));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/quotes'));
  });

  it('hides the delete button from sales once the quote is no longer a draft', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    vi.mocked(quotesApi.getQuote).mockResolvedValue({ ...draft, status: 'sent' });
    render(<QuoteDetail />);
    await screen.findByTestId('quote-number');

    expect(screen.queryByRole('button', { name: 'Delete quote' })).not.toBeInTheDocument();
  });

  it("says so when the quote cannot be loaded (another owner's quote for sales)", async () => {
    vi.mocked(quotesApi.getQuote).mockRejectedValue(new Error('forbidden'));
    render(<QuoteDetail />);

    expect(await screen.findByText('Quote not found')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Back to quotes' }));
    expect(mockNavigate).toHaveBeenCalledWith('/quotes');
  });
});
