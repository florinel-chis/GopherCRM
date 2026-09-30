import { describe, it, expect, vi, beforeEach } from 'vitest';
import { dealsApi } from './deals';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendDeal = {
  id: 9,
  title: 'Website redesign',
  stage: 'proposal',
  amount_cents: 1250000,
  currency: 'EUR',
  probability: 40,
  expected_close_date: '2026-12-15',
  closed_at: null,
  lost_reason: '',
  source: 'referral',
  notes: '',
  company_id: 7,
  owner_id: 1,
  created_at: '2026-09-29T08:00:00Z',
  updated_at: '2026-09-29T08:00:00Z',
};

describe('dealsApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists deals with the filters as params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [backendDeal],
      meta: { total: 31, page: 2, per_page: 10, total_pages: 4 },
    });

    const result = await dealsApi.getDeals({
      offset: 10,
      limit: 10,
      search: 'redesign',
      stage: 'proposal',
      company_id: 7,
      customer_id: 3,
      owner_id: 1,
      open: true,
      sort_by: 'amount_cents',
      sort_order: 'desc',
    });

    expect(api.get).toHaveBeenCalledWith('/deals', {
      params: {
        offset: 10,
        limit: 10,
        search: 'redesign',
        stage: 'proposal',
        company_id: 7,
        customer_id: 3,
        owner_id: 1,
        open: true,
        sort_by: 'amount_cents',
        sort_order: 'desc',
      },
    });
    expect(result.deals).toHaveLength(1);
    expect(result.deals[0].title).toBe('Website redesign');
    expect(result.total).toBe(31);
  });

  it('does not send open=false: the API only knows open=true', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [], meta: { total: 0 } });

    await dealsApi.getDeals({ offset: 0, limit: 20, open: false });

    expect(api.get).toHaveBeenCalledWith('/deals', { params: { offset: 0, limit: 20 } });
  });

  it('falls back to the page length when the response carries no meta', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendDeal, { ...backendDeal, id: 10 }] });

    const result = await dealsApi.getDeals();

    expect(result.total).toBe(2);
  });

  it('returns an empty list when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(dealsApi.getDeals()).resolves.toEqual({ deals: [], total: 0 });
  });

  it('fetches one deal and maps the preloaded customer and lead to the frontend shape', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: {
        ...backendDeal,
        owner: { id: 1, first_name: 'Ana', last_name: 'Pop' },
        company: { id: 7, name: 'Acme Widgets', domain: 'acme.example' },
        customer_id: 3,
        customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets', email: 'jane@acme.example' },
        lead_id: 5,
        lead: { id: 5, first_name: 'Ion', last_name: 'Ionescu', company: 'Bolt', status: 'qualified', email: 'ion@bolt.example' },
      },
    });

    const deal = await dealsApi.getDeal(9);

    expect(api.get).toHaveBeenCalledWith('/deals/9');
    expect(deal.owner?.first_name).toBe('Ana');
    expect(deal.company?.name).toBe('Acme Widgets');
    expect(deal.customer?.contact_name).toBe('Jane Smith');
    expect(deal.customer?.company_name).toBe('Acme Widgets');
    expect(deal.lead?.contact_name).toBe('Ion Ionescu');
    expect(deal.lead?.company_name).toBe('Bolt');
  });

  it('normalises absent optional fields to null and empty strings', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: { id: 9, title: 'Bare', stage: 'qualification', amount_cents: 0, currency: 'EUR', probability: 10, owner_id: 1 },
    });

    const deal = await dealsApi.getDeal(9);

    expect(deal.expected_close_date).toBeNull();
    expect(deal.closed_at).toBeNull();
    expect(deal.lost_reason).toBe('');
    expect(deal.source).toBe('');
    expect(deal.notes).toBe('');
  });

  it('creates a deal with the body as given', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendDeal });

    const created = await dealsApi.createDeal({
      title: 'Website redesign',
      stage: 'proposal',
      amount_cents: 1250000,
      currency: 'EUR',
      probability: 40,
      expected_close_date: '2026-12-15',
      company_id: 7,
    });

    expect(api.post).toHaveBeenCalledWith('/deals', {
      title: 'Website redesign',
      stage: 'proposal',
      amount_cents: 1250000,
      currency: 'EUR',
      probability: 40,
      expected_close_date: '2026-12-15',
      company_id: 7,
    });
    expect(created.id).toBe(9);
  });

  it('propagates an INVALID_REFERENCE rejection to the caller', async () => {
    const rejection = Object.assign(new Error('bad request'), {
      response: {
        status: 400,
        data: { code: 'INVALID_REFERENCE', message: 'unknown company_id 99: company not found', details: null },
      },
    });
    vi.mocked(api.post).mockRejectedValue(rejection);

    await expect(dealsApi.createDeal({ title: 'x', company_id: 99 })).rejects.toBe(rejection);
  });

  it('updates a deal at /deals/{id} with PUT', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendDeal, title: 'Renamed' } });

    const updated = await dealsApi.updateDeal(9, { title: 'Renamed', amount_cents: 100, company_id: 0 });

    expect(api.put).toHaveBeenCalledWith('/deals/9', { title: 'Renamed', amount_cents: 100, company_id: 0 });
    expect(updated.title).toBe('Renamed');
  });

  it('changes the stage at /deals/{id}/stage with the reason and probability', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: { ...backendDeal, stage: 'lost', probability: 0, lost_reason: 'Budget cut', closed_at: '2026-10-01T10:00:00Z' },
    });

    const deal = await dealsApi.changeStage(9, { stage: 'lost', lost_reason: 'Budget cut' });

    expect(api.post).toHaveBeenCalledWith('/deals/9/stage', { stage: 'lost', lost_reason: 'Budget cut' });
    expect(deal.stage).toBe('lost');
    expect(deal.closed_at).toBe('2026-10-01T10:00:00Z');
    expect(deal.lost_reason).toBe('Budget cut');
  });

  it('reads the stage history at /deals/{id}/history', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [
        { id: 1, deal_id: 9, from_stage: null, to_stage: 'qualification', changed_by_id: 1, changed_at: '2026-09-29T08:00:00Z' },
        { id: 2, deal_id: 9, from_stage: 'qualification', to_stage: 'proposal', changed_by_id: 1, changed_at: '2026-09-30T08:00:00Z' },
      ],
    });

    const history = await dealsApi.getDealHistory(9);

    expect(api.get).toHaveBeenCalledWith('/deals/9/history');
    expect(history).toHaveLength(2);
    expect(history[0].from_stage).toBeNull();
    expect(history[1].to_stage).toBe('proposal');
  });

  it('returns an empty history when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(dealsApi.getDealHistory(9)).resolves.toEqual([]);
  });

  it('deletes a deal at /deals/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await dealsApi.deleteDeal(9);

    expect(api.delete).toHaveBeenCalledWith('/deals/9');
  });

  it('lists the deals of a company with offset/limit and the meta total', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendDeal], meta: { total: 4 } });

    const result = await dealsApi.getCompanyDeals(7, { offset: 0, limit: 5 });

    expect(api.get).toHaveBeenCalledWith('/companies/7/deals', { params: { offset: 0, limit: 5 } });
    expect(result.total).toBe(4);
    expect(result.deals[0].id).toBe(9);
  });

  it('lists the deals of a customer with offset/limit and the meta total', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendDeal], meta: { total: 2 } });

    const result = await dealsApi.getCustomerDeals(3, { offset: 5, limit: 5 });

    expect(api.get).toHaveBeenCalledWith('/customers/3/deals', { params: { offset: 5, limit: 5 } });
    expect(result.total).toBe(2);
  });
});
