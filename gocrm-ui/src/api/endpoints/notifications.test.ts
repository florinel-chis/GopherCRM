import { describe, it, expect, vi, beforeEach } from 'vitest';
import { notificationsApi } from './notifications';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendNotification = {
  id: 11,
  created_at: '2026-10-28T09:00:00Z',
  updated_at: '2026-10-28T09:00:00Z',
  user_id: 3,
  title: 'New lead',
  body: 'Call Jane',
  entity_type: 'lead',
  entity_id: 5,
  run_id: 31,
};

describe('notificationsApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists notifications with offset, limit and unread as params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [backendNotification],
      meta: { total: 14, page: 1, per_page: 5, total_pages: 3 },
    });

    const result = await notificationsApi.list({ offset: 0, limit: 5, unread: true });

    expect(api.get).toHaveBeenCalledWith('/notifications', {
      params: { offset: 0, limit: 5, unread: true },
    });
    expect(result).toEqual({ notifications: [backendNotification], total: 14 });

    // unread is a narrowing filter: false asks for everything, like no filter.
    vi.mocked(api.get).mockResolvedValue({ data: [] });
    await expect(notificationsApi.list({ offset: 5, limit: 5, unread: false })).resolves.toEqual({
      notifications: [],
      total: 0,
    });
    expect(api.get).toHaveBeenLastCalledWith('/notifications', {
      params: { offset: 5, limit: 5 },
    });
  });

  it('returns the unread count from the unread field', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { unread: 4 } });

    await expect(notificationsApi.unreadCount()).resolves.toBe(4);
    expect(api.get).toHaveBeenCalledWith('/notifications/unread-count');

    vi.mocked(api.get).mockResolvedValue({ data: {} });
    await expect(notificationsApi.unreadCount()).resolves.toBe(0);
  });

  it('marks one notification read and returns it', async () => {
    const read = { ...backendNotification, read_at: '2026-10-28T10:00:00Z' };
    vi.mocked(api.post).mockResolvedValue({ data: read });

    await expect(notificationsApi.markRead(11)).resolves.toEqual(read);
    expect(api.post).toHaveBeenCalledWith('/notifications/11/read');
  });

  it('marks all read and returns the marked count', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { marked: 6 } });

    await expect(notificationsApi.markAllRead()).resolves.toBe(6);
    expect(api.post).toHaveBeenCalledWith('/notifications/read-all');

    vi.mocked(api.post).mockResolvedValue({ data: {} });
    await expect(notificationsApi.markAllRead()).resolves.toBe(0);
  });
});
