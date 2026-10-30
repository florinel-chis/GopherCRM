import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../client';
import { leadsApi } from './leads';

vi.mock('../client', () => ({ api: { post: vi.fn(), put: vi.fn() } }));

beforeEach(() => {
  vi.mocked(api.post).mockReset();
  vi.mocked(api.put).mockReset();
});

it('sends the conversion website and returns the created customer id for navigation', async () => {
  vi.mocked(api.post).mockResolvedValue({ data: { id: 42, website: 'https://example.com' } });
  await expect(leadsApi.convertLead(7, { website: 'https://example.com' })).resolves.toEqual({
    customer_id: 42,
  });
  expect(api.post).toHaveBeenCalledWith('/leads/7/convert', { website: 'https://example.com' });
});

// The API reassigns a lead only when owner_id is present (admin only; sales
// gets 403). The client must forward an explicit owner and must not invent
// one when the caller left it out.
describe('updateLead owner_id', () => {
  const savedLead = {
    id: 5,
    first_name: 'Ada',
    last_name: 'Lovelace',
    company: 'Analytical',
    owner_id: 9,
  };

  it('sends owner_id when the caller provides it', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: savedLead });

    const lead = await leadsApi.updateLead(5, { owner_id: 9, status: 'contacted' });

    expect(api.put).toHaveBeenCalledWith('/leads/5', { owner_id: 9, status: 'contacted' });
    expect(lead.owner_id).toBe(9);
  });

  it('omits owner_id when the caller leaves it out', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...savedLead, owner_id: 3 } });

    await leadsApi.updateLead(5, { status: 'contacted', company_name: 'Analytical' });

    const payload = vi.mocked(api.put).mock.calls[0][1] as Record<string, unknown>;
    expect(payload).toEqual({ status: 'contacted', company: 'Analytical' });
    expect('owner_id' in payload).toBe(false);
  });
});
