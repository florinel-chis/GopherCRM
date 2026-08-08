import { describe, it, expect, vi, beforeEach } from 'vitest';
import { labelsApi } from './labels';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendLabel = {
  id: 4,
  name: 'Q3 initiative',
  color: '#1F77B4',
  task_count: 7,
};

describe('labelsApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists labels from the bare array at /labels', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendLabel] });

    const result = await labelsApi.getLabels();

    expect(api.get).toHaveBeenCalledWith('/labels');
    expect(result).toHaveLength(1);
    expect(result[0]).toEqual(backendLabel);
    expect(result[0].task_count).toBe(7);
  });

  it('returns an empty list when the response is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(labelsApi.getLabels()).resolves.toEqual([]);
  });

  it('creates a label with a name and colour', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendLabel });

    const created = await labelsApi.createLabel({ name: 'Q3 initiative', color: '#1F77B4' });

    expect(api.post).toHaveBeenCalledWith('/labels', {
      name: 'Q3 initiative',
      color: '#1F77B4',
    });
    expect(created.id).toBe(4);
  });

  it('propagates a 409 duplicate-name rejection to the caller', async () => {
    const conflict = Object.assign(new Error('conflict'), {
      response: { status: 409 },
    });
    vi.mocked(api.post).mockRejectedValue(conflict);

    await expect(labelsApi.createLabel({ name: 'Q3 initiative', color: '#1F77B4' })).rejects.toBe(
      conflict
    );
  });

  it('updates a label at /labels/{id}', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendLabel, color: '#2CA02C' } });

    const updated = await labelsApi.updateLabel(4, { color: '#2CA02C' });

    expect(api.put).toHaveBeenCalledWith('/labels/4', { color: '#2CA02C' });
    expect(updated.color).toBe('#2CA02C');
  });

  it('deletes a label at /labels/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await labelsApi.deleteLabel(4);

    expect(api.delete).toHaveBeenCalledWith('/labels/4');
  });
});
