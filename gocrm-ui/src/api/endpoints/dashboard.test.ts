import { describe, it, expect, vi, beforeEach } from 'vitest';
import { dashboardApi } from './dashboard';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
  },
}));

describe('dashboardApi.getPipeline', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('reads the stages and the deals won this month per currency', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: {
        stages: [
          { stage: 'qualification', count: 1, totals: [{ currency: 'EUR', amount_cents: 100000, weighted_cents: 10000 }] },
          { stage: 'proposal', count: 0, totals: [] },
          { stage: 'negotiation', count: 0, totals: [] },
          { stage: 'won', count: 2, totals: [{ currency: 'EUR', amount_cents: 500000, weighted_cents: 500000 }] },
          { stage: 'lost', count: 0, totals: [] },
        ],
        won_this_month: {
          count: 2,
          totals: [
            { currency: 'EUR', amount_cents: 400000 },
            { currency: 'USD', amount_cents: 100000 },
          ],
        },
      },
    });

    const result = await dashboardApi.getPipeline();

    expect(api.get).toHaveBeenCalledWith('/dashboard/pipeline');
    expect(result.stages.map((stage) => [stage.stage, stage.count])).toEqual([
      ['qualification', 1],
      ['proposal', 0],
      ['negotiation', 0],
      ['won', 2],
      ['lost', 0],
    ]);
    expect(result.won_this_month).toEqual({
      count: 2,
      totals: [
        { currency: 'EUR', amount_cents: 400000 },
        { currency: 'USD', amount_cents: 100000 },
      ],
    });
  });

  it('reads a missing won_this_month and null totals as empty', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: { stages: [{ stage: 'proposal', count: 0, totals: null }] },
    });

    const result = await dashboardApi.getPipeline();

    expect(result.stages).toHaveLength(5);
    expect(result.stages[1]).toEqual({ stage: 'proposal', count: 0, totals: [] });
    expect(result.won_this_month).toEqual({ count: 0, totals: [] });
  });
});
