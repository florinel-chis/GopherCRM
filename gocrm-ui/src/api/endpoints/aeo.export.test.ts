import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../client';
import { aeoApi } from './aeo';

vi.mock('../client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

beforeEach(() => {
  vi.mocked(api.get).mockReset();
});

describe('aeoApi.exportResults', () => {
  it('requests a days window as a blob without run_id', async () => {
    const blob = new Blob(['run_id,answer_id\n'], { type: 'text/csv' });
    vi.mocked(api.get).mockResolvedValue({ data: blob });

    const result = await aeoApi.exportResults({ format: 'csv', days: 7 });

    expect(result).toBe(blob);
    expect(api.get).toHaveBeenCalledTimes(1);
    const [url, config] = vi.mocked(api.get).mock.calls[0] as [
      string,
      { params: Record<string, unknown>; responseType: string },
    ];
    expect(url).toBe('/aeo/export');
    expect(config.responseType).toBe('blob');
    expect(config.params).toStrictEqual({ format: 'csv', days: 7 });
    expect('run_id' in config.params).toBe(false);
  });

  it('requests a single run as a blob without days', async () => {
    const blob = new Blob(['{"format_version":1}'], { type: 'application/json' });
    vi.mocked(api.get).mockResolvedValue({ data: blob });

    const result = await aeoApi.exportResults({ format: 'json', run_id: 42 });

    expect(result).toBe(blob);
    expect(api.get).toHaveBeenCalledTimes(1);
    const [url, config] = vi.mocked(api.get).mock.calls[0] as [
      string,
      { params: Record<string, unknown>; responseType: string },
    ];
    expect(url).toBe('/aeo/export');
    expect(config.responseType).toBe('blob');
    expect(config.params).toStrictEqual({ format: 'json', run_id: 42 });
    expect('days' in config.params).toBe(false);
  });
});
