import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { DealQuotesSection } from './DealQuotesSection';
import { configurationsApi, quotesApi } from '@/api/endpoints';
import { createMockCompany, createMockDeal, createMockQuote } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import { formatMoney } from './quoteFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  quotesApi: {
    getDealQuotes: vi.fn(),
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

describe('DealQuotesSection', () => {
  const mockNavigate = vi.fn();
  const deal = createMockDeal({
    id: 9,
    title: 'Website redesign',
    stage: 'proposal',
    currency: 'EUR',
    company_id: 7,
    company: createMockCompany({ id: 7, name: 'Acme Widgets' }),
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
    vi.mocked(quotesApi.getDealQuotes).mockResolvedValue({
      quotes: [
        createMockQuote({ id: 42, number: 'Q-000042', title: 'First offer', status: 'accepted', total_cents: 225000, currency: 'EUR' }),
        createMockQuote({ id: 43, number: 'Q-000043', title: 'Second offer', status: 'draft', total_cents: 0, currency: 'EUR' }),
      ],
      total: 2,
    });
  });

  it("lists the deal's quotes with status and total", async () => {
    render(<DealQuotesSection deal={deal} canWrite />);

    expect(await screen.findByRole('heading', { name: 'Quotes (2)' })).toBeInTheDocument();
    await waitFor(() => expect(quotesApi.getDealQuotes).toHaveBeenCalledWith(9, { offset: 0, limit: 5 }));

    const rows = screen.getAllByTestId('deal-quote-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('link', { name: 'Q-000042' })).toHaveAttribute('href', '/quotes/42');
    expect(within(rows[0]).getByText('First offer')).toBeInTheDocument();
    expect(within(rows[0]).getByTestId('quote-status-chip')).toHaveTextContent('Accepted');
    expect(within(rows[0]).getByText(formatMoney(225000, 'EUR'))).toBeInTheDocument();
    expect(within(rows[1]).getByTestId('quote-status-chip')).toHaveTextContent('Draft');
    expect(within(rows[1]).getByText(formatMoney(0, 'EUR'))).toBeInTheDocument();
  });

  it('hides New quote for a won or lost deal', async () => {
    const won = render(<DealQuotesSection deal={{ ...deal, stage: 'won' }} canWrite />);
    await screen.findByRole('heading', { name: 'Quotes (2)' });
    expect(screen.queryByRole('button', { name: 'New quote' })).not.toBeInTheDocument();
    won.unmount();

    const lost = render(<DealQuotesSection deal={{ ...deal, stage: 'lost' }} canWrite />);
    await screen.findByRole('heading', { name: 'Quotes (2)' });
    expect(screen.queryByRole('button', { name: 'New quote' })).not.toBeInTheDocument();
    lost.unmount();

    render(<DealQuotesSection deal={deal} canWrite />);
    expect(await screen.findByRole('button', { name: 'New quote' })).toBeInTheDocument();
  });

  it('hides New quote when the viewer cannot write', async () => {
    render(<DealQuotesSection deal={deal} canWrite={false} />);

    await screen.findByRole('heading', { name: 'Quotes (2)' });
    expect(screen.queryByRole('button', { name: 'New quote' })).not.toBeInTheDocument();
  });

  it('opens the create dialog prefilled with the deal and goes to the new quote', async () => {
    const created = createMockQuote({ id: 44, number: 'Q-000044', title: 'Third offer' });
    vi.mocked(quotesApi.createQuote).mockResolvedValue(created);
    render(<DealQuotesSection deal={deal} canWrite />);

    fireEvent.click(await screen.findByRole('button', { name: 'New quote' }));
    const dialog = await screen.findByRole('dialog', { name: 'New quote' });
    expect(within(dialog).getByRole('combobox', { name: 'Deal' })).toHaveValue('Website redesign (Acme Widgets)');

    fireEvent.change(within(dialog).getByLabelText(/^Title/), { target: { value: 'Third offer' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() =>
      expect(quotesApi.createQuote).toHaveBeenCalledWith({ title: 'Third offer', currency: 'EUR', deal_id: 9 })
    );
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/quotes/44'));
  });

  it('shows the empty text when the deal has no quotes', async () => {
    vi.mocked(quotesApi.getDealQuotes).mockResolvedValue({ quotes: [], total: 0 });
    render(<DealQuotesSection deal={deal} canWrite />);

    expect(await screen.findByText('No quotes for this deal')).toBeInTheDocument();
  });
});
