import { describe, it, expect, vi, beforeEach } from 'vitest';
import { quotesApi } from './quotes';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendQuote = {
  id: 42,
  number: 'Q-000042',
  title: 'Website redesign quote',
  status: 'draft',
  currency: 'EUR',
  subtotal_cents: 2500,
  discount_cents: 250,
  total_cents: 2250,
  owner_id: 1,
  owner: { id: 1, first_name: 'Ana', last_name: 'Pop' },
  deal_id: 9,
  deal: { id: 9, title: 'Website redesign', stage: 'proposal' },
  customer_id: 3,
  customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
  lines: [
    {
      id: 7,
      quote_id: 42,
      product_id: 1,
      position: 1,
      description: 'Widget',
      quantity: 2,
      unit_price_cents: 1250,
      discount_bp: 1000,
      line_total_cents: 2250,
      created_at: '2026-10-26T08:00:00Z',
      updated_at: '2026-10-26T08:00:00Z',
    },
  ],
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
};

describe('quotesApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists quotes with the filters as params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [{ ...backendQuote, lines: undefined }],
      meta: { total: 31, page: 2, per_page: 10, total_pages: 4 },
    });

    const result = await quotesApi.getQuotes({
      offset: 10,
      limit: 10,
      status: 'sent',
      deal_id: 9,
      customer_id: 3,
      sort_by: 'total_cents',
      sort_order: 'desc',
    });

    expect(api.get).toHaveBeenCalledWith('/quotes', {
      params: {
        offset: 10,
        limit: 10,
        status: 'sent',
        deal_id: 9,
        customer_id: 3,
        sort_by: 'total_cents',
        sort_order: 'desc',
      },
    });
    expect(result.quotes).toHaveLength(1);
    expect(result.quotes[0].number).toBe('Q-000042');
    // The list carries no lines: they read as empty.
    expect(result.quotes[0].lines).toEqual([]);
    expect(result.total).toBe(31);
  });

  it('returns an empty list when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(quotesApi.getQuotes()).resolves.toEqual({ quotes: [], total: 0 });
  });

  it('fetches one quote with its lines, summaries and server totals', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: backendQuote });

    const quote = await quotesApi.getQuote(42);

    expect(api.get).toHaveBeenCalledWith('/quotes/42');
    expect(quote.lines).toHaveLength(1);
    expect(quote.lines[0].line_total_cents).toBe(2250);
    expect(quote.total_cents).toBe(2250);
    expect(quote.customer?.company).toBe('Acme Widgets');
    expect(quote.deal?.title).toBe('Website redesign');
    expect(quote.owner?.first_name).toBe('Ana');
  });

  it('normalises absent optional fields to null and empty strings', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: backendQuote });

    const quote = await quotesApi.getQuote(42);

    expect(quote.valid_until).toBeNull();
    expect(quote.notes).toBe('');
    expect(quote.sent_at).toBeNull();
    expect(quote.closed_at).toBeNull();
    expect(quote.last_emailed_at).toBeNull();
  });

  it('creates a quote with the body as given', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendQuote });

    const created = await quotesApi.createQuote({
      title: 'Website redesign quote',
      deal_id: 9,
      currency: 'EUR',
      valid_until: '2026-12-31',
      notes: 'Net 30',
    });

    expect(api.post).toHaveBeenCalledWith('/quotes', {
      title: 'Website redesign quote',
      deal_id: 9,
      currency: 'EUR',
      valid_until: '2026-12-31',
      notes: 'Net 30',
    });
    expect(created.id).toBe(42);
  });

  it('updates a draft at /quotes/{id} with PUT, null clearing valid_until', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendQuote, title: 'Renamed' } });

    const updated = await quotesApi.updateQuote(42, { title: 'Renamed', valid_until: null });

    expect(api.put).toHaveBeenCalledWith('/quotes/42', { title: 'Renamed', valid_until: null });
    expect(updated.title).toBe('Renamed');
  });

  it('deletes a quote at /quotes/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await quotesApi.deleteQuote(42);

    expect(api.delete).toHaveBeenCalledWith('/quotes/42');
  });

  it('adds a line in basis points and returns the quote with its new totals', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendQuote });

    const quote = await quotesApi.addLine(42, { product_id: 1, quantity: 2, discount_bp: 1250 });

    expect(api.post).toHaveBeenCalledWith('/quotes/42/lines', { product_id: 1, quantity: 2, discount_bp: 1250 });
    expect(quote.subtotal_cents).toBe(2500);
  });

  it('edits and removes a line at /quotes/{id}/lines/{lineId}', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: backendQuote });
    vi.mocked(api.delete).mockResolvedValue({ data: { ...backendQuote, lines: [], total_cents: 0 } });

    await quotesApi.updateLine(42, 7, { quantity: 3 });
    const afterDelete = await quotesApi.deleteLine(42, 7);

    expect(api.put).toHaveBeenCalledWith('/quotes/42/lines/7', { quantity: 3 });
    expect(api.delete).toHaveBeenCalledWith('/quotes/42/lines/7');
    expect(afterDelete.lines).toEqual([]);
    expect(afterDelete.total_cents).toBe(0);
  });

  it('changes the status at /quotes/{id}/status', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: { ...backendQuote, status: 'sent', sent_at: '2026-10-27T10:00:00Z' },
    });

    const quote = await quotesApi.changeStatus(42, 'sent');

    expect(api.post).toHaveBeenCalledWith('/quotes/42/status', { status: 'sent' });
    expect(quote.status).toBe('sent');
    expect(quote.sent_at).toBe('2026-10-27T10:00:00Z');
  });

  it('propagates an INVALID_STATUS_TRANSITION rejection to the caller', async () => {
    const rejection = Object.assign(new Error('bad request'), {
      response: {
        status: 400,
        data: { code: 'INVALID_STATUS_TRANSITION', message: 'invalid quote status transition', details: null },
      },
    });
    vi.mocked(api.post).mockRejectedValue(rejection);

    await expect(quotesApi.changeStatus(42, 'accepted')).rejects.toBe(rejection);
  });

  it('reads the status history at /quotes/{id}/history', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [
        { id: 1, quote_id: 42, from_status: null, to_status: 'draft', changed_by_id: 1, changed_at: '2026-10-26T08:00:00Z' },
        { id: 2, quote_id: 42, from_status: 'draft', to_status: 'sent', changed_by_id: 1, changed_at: '2026-10-27T08:00:00Z' },
      ],
    });

    const history = await quotesApi.getHistory(42);

    expect(api.get).toHaveBeenCalledWith('/quotes/42/history');
    expect(history).toHaveLength(2);
    expect(history[0].from_status).toBeNull();
    expect(history[1].to_status).toBe('sent');
  });

  it('returns an empty history when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(quotesApi.getHistory(42)).resolves.toEqual([]);
  });

  it('emails the quote with an empty body and returns the stamped quote', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: { ...backendQuote, status: 'sent', last_emailed_at: '2026-10-27T12:00:00Z' },
    });

    const quote = await quotesApi.emailQuote(42);

    expect(api.post).toHaveBeenCalledWith('/quotes/42/email');
    expect(quote.last_emailed_at).toBe('2026-10-27T12:00:00Z');
  });

  it('lists the quotes of a deal and of a customer with the meta total', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendQuote], meta: { total: 4 } });

    const byDeal = await quotesApi.getDealQuotes(9, { offset: 0, limit: 5 });
    const byCustomer = await quotesApi.getCustomerQuotes(3, { offset: 5, limit: 5 });

    expect(api.get).toHaveBeenCalledWith('/deals/9/quotes', { params: { offset: 0, limit: 5 } });
    expect(api.get).toHaveBeenCalledWith('/customers/3/quotes', { params: { offset: 5, limit: 5 } });
    expect(byDeal.total).toBe(4);
    expect(byCustomer.quotes[0].id).toBe(42);
  });
});
