import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as QuoteList } from './QuoteList';
import { configurationsApi, quotesApi } from '@/api/endpoints';
import { createMockQuote } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import { formatMoney } from './quoteFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    getQuotes: vi.fn(),
    createQuote: vi.fn(),
  },
  dealsApi: {
    getDeals: vi.fn(),
    getDeal: vi.fn(),
  },
  customersApi: {
    getCustomers: vi.fn(),
    getCustomer: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}));

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

describe('QuoteList', () => {
  const mockNavigate = vi.fn();
  const draft = createMockQuote({
    id: 42,
    number: 'Q-000042',
    title: 'Website redesign quote',
    status: 'draft',
    currency: 'EUR',
    total_cents: 225000,
    valid_until: '2099-12-31',
    customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
    deal: { id: 9, title: 'Website redesign', stage: 'proposal' },
  });
  const sent = createMockQuote({
    id: 43,
    number: 'Q-000043',
    title: 'Support renewal quote',
    status: 'sent',
    currency: 'USD',
    total_cents: 99900,
    valid_until: null,
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(quotesApi.getQuotes).mockResolvedValue({ quotes: [draft, sent], total: 2 });
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
  });

  it('renders number, title, status, customer, deal, total and valid-until from the quotes response', async () => {
    render(<QuoteList />);

    const first = await rowOf('Q-000042');
    expect(within(first).getByText('Website redesign quote')).toBeInTheDocument();
    expect(within(first).getByTestId('quote-status-chip')).toHaveTextContent('Draft');
    expect(within(first).getByText('Jane Smith (Acme Widgets)')).toBeInTheDocument();
    expect(within(first).getByText('Website redesign')).toBeInTheDocument();
    expect(within(first).getByText(formatMoney(225000, 'EUR'))).toBeInTheDocument();
    expect(within(first).getByText('Dec 31, 2099')).toBeInTheDocument();

    const second = await rowOf('Q-000043');
    expect(within(second).getByTestId('quote-status-chip')).toHaveTextContent('Sent');
    expect(within(second).getByText(formatMoney(99900, 'USD'))).toBeInTheDocument();
    // No customer, no deal, no date: placeholders.
    expect(within(second).getAllByText('—')).toHaveLength(3);
  });

  it('requests the first page with offset/limit and no status filter', async () => {
    render(<QuoteList />);

    await waitFor(() => expect(quotesApi.getQuotes).toHaveBeenCalledTimes(1));
    expect(quotesApi.getQuotes).toHaveBeenCalledWith({
      offset: 0,
      limit: 10,
      status: undefined,
      sort_by: undefined,
      sort_order: undefined,
    });
  });

  it('refetches with status=sent when the status filter changes', async () => {
    render(<QuoteList />);
    await rowOf('Q-000042');

    fireEvent.mouseDown(screen.getByRole('combobox', { name: /Status/ }));
    fireEvent.click(await screen.findByRole('option', { name: 'Sent' }));

    await waitFor(() =>
      expect(quotesApi.getQuotes).toHaveBeenLastCalledWith(expect.objectContaining({ status: 'sent', offset: 0 }))
    );
  });

  it('pages and sorts server-side, with no sort on the customer and deal columns', async () => {
    vi.mocked(quotesApi.getQuotes).mockResolvedValue({ quotes: [draft, sent], total: 25 });
    render(<QuoteList />);
    await rowOf('Q-000042');

    fireEvent.click(screen.getByRole('button', { name: /^Total/ }));
    await waitFor(() =>
      expect(quotesApi.getQuotes).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'total_cents', sort_order: 'asc' })
      )
    );

    await rowOf('Q-000042');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(quotesApi.getQuotes).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 10 })));

    expect(screen.queryByRole('button', { name: /^Customer/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Deal/ })).not.toBeInTheDocument();
  });

  it('opens the detail page from a row and the create dialog from New Quote', async () => {
    render(<QuoteList />);

    fireEvent.click(await screen.findByText('Website redesign quote'));
    expect(mockNavigate).toHaveBeenCalledWith('/quotes/42');

    fireEvent.click(screen.getByRole('button', { name: 'New Quote' }));
    expect(await screen.findByRole('dialog', { name: 'New quote' })).toBeInTheDocument();
  });
});
