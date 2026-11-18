import { describe, it, expect, vi, beforeEach } from 'vitest';
import { quoteRenderApi } from './quoteRender';
import { quoteRenderApi as exportedFromIndex } from './index';
import { api, publicApi } from '../client';

vi.mock('../client', () => ({
  api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() },
  publicApi: { get: vi.fn(), post: vi.fn() },
}));

const backendQuote = {
  id: 42,
  number: 'Q-000042',
  title: 'Website redesign quote',
  status: 'sent',
  currency: 'EUR',
  subtotal_cents: 2500,
  discount_cents: 250,
  total_cents: 2250,
  tax_cents: 0,
  gross_total_cents: 2250,
  owner_id: 1,
  customer_id: 3,
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
};

describe('quoteRenderApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('posts the credential in the body on the public client', async () => {
    vi.mocked(publicApi.post).mockResolvedValue({ data: { quote: backendQuote, product_name: 'Acme CRM' } });

    const view = await quoteRenderApi.getQuoteRender('credential');

    expect(publicApi.post).toHaveBeenCalledTimes(1);
    expect(publicApi.post).toHaveBeenCalledWith('/quotes/public/render', { token: 'credential' });
    expect(view.product_name).toBe('Acme CRM');
    expect(view.quote.number).toBe('Q-000042');
    // No session client and no URL carries the credential.
    expect(api.get).not.toHaveBeenCalled();
    expect(api.post).not.toHaveBeenCalled();
    expect(publicApi.get).not.toHaveBeenCalled();
  });

  it('normalizes the quote like GET /quotes/:id and a missing product name to empty', async () => {
    vi.mocked(publicApi.post).mockResolvedValue({ data: { quote: backendQuote } });

    const view = await quoteRenderApi.getQuoteRender('credential');

    expect(view.product_name).toBe('');
    expect(view.quote.lines).toEqual([]);
    expect(view.quote.valid_until).toBeNull();
    expect(view.quote.notes).toBe('');
    expect(view.quote.sent_at).toBeNull();
  });

  it('passes the rejection on', async () => {
    const rejection = { response: { status: 400, data: { code: 'BAD_REQUEST', message: 'Invalid or expired render link' } } };
    vi.mocked(publicApi.post).mockRejectedValue(rejection);

    await expect(quoteRenderApi.getQuoteRender('spent')).rejects.toBe(rejection);
  });

  it('is exported from the endpoints index', () => {
    expect(exportedFromIndex).toBe(quoteRenderApi);
  });
});
