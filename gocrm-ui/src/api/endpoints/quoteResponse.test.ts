import { describe, it, expect, vi, beforeEach } from 'vitest';
import { quoteResponseApi } from './quoteResponse';
import { api, publicApi } from '../client';

vi.mock('../client', () => ({
  api: { get: vi.fn(), post: vi.fn() },
  publicApi: { get: vi.fn(), post: vi.fn() },
}));

const backendView = {
  number: 'Q-000042',
  status: 'sent',
  currency: 'EUR',
  valid_until: '2026-12-31',
  lines: [
    {
      description: 'Widget',
      quantity: 2,
      unit_price_cents: 1250,
      discount_bp: 1000,
      line_total_cents: 2250,
      tax_rate_bp: 1900,
      tax_cents: 428,
    },
  ],
  subtotal_cents: 2500,
  discount_cents: 250,
  total_cents: 2250,
  tax_cents: 428,
  gross_total_cents: 2678,
};

describe('quoteResponseApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('reads the quote with the token in the X-Quote-Token header on the public client', async () => {
    vi.mocked(publicApi.get).mockResolvedValue({ data: backendView });

    const view = await quoteResponseApi.getQuoteResponse('t');

    expect(publicApi.get).toHaveBeenCalledTimes(1);
    expect(publicApi.get).toHaveBeenCalledWith('/quotes/public/response', {
      headers: { 'X-Quote-Token': 't' },
    });
    // The token is never a query parameter.
    expect(vi.mocked(publicApi.get).mock.calls[0][1]).not.toHaveProperty('params');
    expect(view).toEqual(backendView);
    expect(api.get).not.toHaveBeenCalled();
  });

  it('posts the token and the decision on the public client', async () => {
    vi.mocked(publicApi.post).mockResolvedValue({ data: { ...backendView, status: 'accepted' } });

    const view = await quoteResponseApi.respond('t', 'accept');

    expect(publicApi.post).toHaveBeenCalledTimes(1);
    expect(publicApi.post).toHaveBeenCalledWith('/quotes/public/response', { token: 't', decision: 'accept' });
    expect(view.status).toBe('accepted');
    expect(api.post).not.toHaveBeenCalled();

    vi.mocked(publicApi.post).mockResolvedValue({ data: { ...backendView, status: 'rejected' } });
    await quoteResponseApi.respond('t', 'decline');
    expect(publicApi.post).toHaveBeenLastCalledWith('/quotes/public/response', { token: 't', decision: 'decline' });
  });

  it('normalises an omitted valid_until to null and missing lines to an empty array', async () => {
    const { valid_until: _validUntil, ...withoutDate } = backendView;
    void _validUntil;
    vi.mocked(publicApi.get).mockResolvedValue({ data: { ...withoutDate, lines: null } });

    const view = await quoteResponseApi.getQuoteResponse('t');

    expect(view.valid_until).toBeNull();
    expect(view.lines).toEqual([]);
  });

  it('passes a rejection through unchanged', async () => {
    const rejection = { response: { status: 400, data: { code: 'BAD_REQUEST', message: 'Invalid or expired quote link' } } };
    vi.mocked(publicApi.get).mockRejectedValue(rejection);

    await expect(quoteResponseApi.getQuoteResponse('t')).rejects.toBe(rejection);
  });
});
