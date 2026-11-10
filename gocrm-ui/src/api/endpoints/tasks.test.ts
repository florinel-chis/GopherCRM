import { describe, it, expect, vi, beforeEach } from 'vitest';
import { tasksApi } from './tasks';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const assignee = {
  id: 4,
  email: 'ana@example.com',
  first_name: 'Ana',
  last_name: 'Pop',
  role: 'sales' as const,
  is_active: true,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

const backendTask = {
  id: 31,
  title: 'Send the proposal',
  description: '',
  status: 'pending' as const,
  priority: 'high' as const,
  due_date: '2026-11-20T00:00:00Z',
  assigned_to_id: 4,
  assigned_to: assignee,
  deal_id: 9,
  deal: { id: 9, title: 'Website redesign', stage: 'won' as const },
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-02T00:00:00Z',
};

const baseCreate = {
  title: 'Send the proposal',
  description: '',
  priority: 'high' as const,
  due_date: '2026-11-20T00:00:00Z',
};

describe('tasksApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.post).mockResolvedValue({ data: backendTask });
    vi.mocked(api.put).mockResolvedValue({ data: backendTask });
  });

  describe('deal link', () => {
    it('reads the tasks of a deal from GET /deals/{id}/tasks', async () => {
      vi.mocked(api.get).mockResolvedValue({
        data: {
          tasks: [backendTask, { ...backendTask, id: 32, deal: undefined, labels: undefined }],
          total: 12,
        },
      });

      const result = await tasksApi.getDealTasks(9, { offset: 5, limit: 5 });

      expect(api.get).toHaveBeenCalledWith('/deals/9/tasks', {
        params: { offset: 5, limit: 5 },
      });
      expect(result.total).toBe(12);
      expect(result.tasks).toHaveLength(2);
      const [first, second] = result.tasks;
      // Transformed like getTask: assigned_to mapped, labels normalised.
      expect(first.assigned_to).toBe(4);
      expect(first.labels).toEqual([]);
      expect(first.deal_id).toBe(9);
      expect(first.deal).toEqual({ id: 9, title: 'Website redesign', stage: 'won' });
      expect(second.id).toBe(32);
      expect(second.deal_id).toBe(9);
      expect(second.deal).toBeUndefined();
      expect(second.labels).toEqual([]);

      // Same transform as the single read.
      vi.mocked(api.get).mockResolvedValue({ data: backendTask });
      expect(await tasksApi.getTask(31)).toEqual(first);
    });

    it('sends deal_id on create and update only when given', async () => {
      await tasksApi.createTask({ ...baseCreate, deal_id: 9 });
      expect(vi.mocked(api.post).mock.calls[0][0]).toBe('/tasks');
      expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ deal_id: 9 });

      await tasksApi.createTask({ ...baseCreate, deal_id: 0 });
      expect(vi.mocked(api.post).mock.calls[1][1]).toMatchObject({ deal_id: 0 });

      await tasksApi.createTask(baseCreate);
      expect(vi.mocked(api.post).mock.calls[2][1]).not.toHaveProperty('deal_id');

      await tasksApi.updateTask(31, { title: 'Renamed', deal_id: 7 });
      expect(api.put).toHaveBeenLastCalledWith('/tasks/31', { title: 'Renamed', deal_id: 7 });

      await tasksApi.updateTask(31, { title: 'Renamed', deal_id: 0 });
      expect(api.put).toHaveBeenLastCalledWith('/tasks/31', { title: 'Renamed', deal_id: 0 });

      await tasksApi.updateTask(31, { title: 'Renamed' });
      expect(vi.mocked(api.put).mock.calls[2][1]).toEqual({ title: 'Renamed' });
      expect(vi.mocked(api.put).mock.calls[2][1]).not.toHaveProperty('deal_id');

      // An explicit undefined is not sent as a key either.
      await tasksApi.updateTask(31, { title: 'Renamed', deal_id: undefined });
      expect(vi.mocked(api.put).mock.calls[3][1]).not.toHaveProperty('deal_id');
    });
  });
});
