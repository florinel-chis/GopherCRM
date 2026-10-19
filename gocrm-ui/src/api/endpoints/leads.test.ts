import { expect, it, vi } from 'vitest';
import { api } from '../client';
import { leadsApi } from './leads';

vi.mock('../client', () => ({ api: { post: vi.fn() } }));

it('sends the conversion website and returns the created customer id for navigation', async () => {
  vi.mocked(api.post).mockResolvedValue({ data: { id: 42, website: 'https://example.com' } });
  await expect(leadsApi.convertLead(7, { website: 'https://example.com' })).resolves.toEqual({
    customer_id: 42,
  });
  expect(api.post).toHaveBeenCalledWith('/leads/7/convert', { website: 'https://example.com' });
});
