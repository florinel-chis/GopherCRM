import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@/test/test-utils';
import { Component as NotificationList } from './NotificationList';
import { notificationsApi } from '@/api/endpoints';
import { createMockNotification, createMockUser } from '@/test/factories';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { isInvalidated, withClient } from '@/test/dealQueryCache';
import { notificationKeys } from '@/pages/automation/automationQueries';
import type { User } from '@/types';

vi.mock('@/api/endpoints/notifications', () => ({
  notificationsApi: {
    list: vi.fn(),
    unreadCount: vi.fn(),
    markRead: vi.fn(),
    markAllRead: vi.fn(),
  },
}));

const mockShowSuccess = vi.fn();
const mockShowError = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: mockShowSuccess, showError: mockShowError }),
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

describe('NotificationList', () => {
  const unread = createMockNotification({
    id: 11,
    title: 'Call Jane back',
    body: 'Lead #5 asked for a call',
    entity_type: 'lead',
    entity_id: 5,
    created_at: '2026-10-28T09:30:00Z',
  });
  const read = createMockNotification({
    id: 12,
    title: 'Deal moved',
    body: 'Deal #9 reached proposal',
    entity_type: 'deal',
    entity_id: 9,
    read_at: '2026-10-28T10:00:00Z',
  });

  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));
    vi.mocked(notificationsApi.list).mockResolvedValue({ notifications: [unread, read], total: 25 });
    vi.mocked(notificationsApi.markRead).mockResolvedValue({ ...unread, read_at: '2026-10-29T09:00:00Z' });
    vi.mocked(notificationsApi.markAllRead).mockResolvedValue(4);
    vi.mocked(notificationsApi.unreadCount).mockResolvedValue(0);
  });

  it('lists notifications with server pagination', async () => {
    render(<NotificationList />);

    expect(await screen.findByRole('heading', { name: 'Notifications' })).toBeInTheDocument();
    const first = await rowOf('Call Jane back');
    expect(within(first).getByText('Lead #5 asked for a call')).toBeInTheDocument();
    expect(within(first).getByText('Unread')).toBeInTheDocument();
    expect(within(first).getByRole('button', { name: 'Mark read' })).toBeInTheDocument();
    const second = await rowOf('Deal moved');
    expect(within(second).getByText('Read')).toBeInTheDocument();
    expect(within(second).queryByRole('button', { name: 'Mark read' })).not.toBeInTheDocument();
    expect(screen.getAllByTestId('notification-row')).toHaveLength(2);
    expect(notificationsApi.list).toHaveBeenLastCalledWith({ offset: 0, limit: 10 });
    expect(screen.getByText('1–10 of 25')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Go to next page' }));
    await waitFor(() =>
      expect(notificationsApi.list).toHaveBeenLastCalledWith({ offset: 10, limit: 10 }),
    );

    fireEvent.mouseDown(screen.getByRole('combobox'));
    fireEvent.click(await screen.findByRole('option', { name: '25' }));
    await waitFor(() =>
      expect(notificationsApi.list).toHaveBeenLastCalledWith({ offset: 0, limit: 25 }),
    );
  });

  it('the unread filter sends unread=true', async () => {
    render(<NotificationList />);
    await rowOf('Call Jane back');

    const filter = screen.getByRole('group', { name: 'Filter' });
    fireEvent.click(within(filter).getByRole('button', { name: 'Unread' }));
    await waitFor(() =>
      expect(notificationsApi.list).toHaveBeenLastCalledWith({ offset: 0, limit: 10, unread: true }),
    );

    fireEvent.click(within(filter).getByRole('button', { name: 'All' }));
    await waitFor(() =>
      expect(notificationsApi.list).toHaveBeenLastCalledWith({ offset: 0, limit: 10 }),
    );
    expect(vi.mocked(notificationsApi.list).mock.lastCall?.[0]?.unread).toBeUndefined();
  });

  it('marks one notification read and the bell count query is invalidated', async () => {
    const client = createProductionQueryClient();
    // The bell's count, cached as it would be in the app bar.
    client.setQueryData(notificationKeys.unreadCount, 3);
    render(withClient(client, <NotificationList />));

    const row = await rowOf('Call Jane back');
    fireEvent.click(within(row).getByRole('button', { name: 'Mark read' }));

    await waitFor(() => expect(notificationsApi.markRead).toHaveBeenCalledWith(11));
    await waitFor(() => expect(isInvalidated(client, notificationKeys.unreadCount)).toBe(true));
    // The page itself refetches despite the five-minute default.
    await waitFor(() => expect(notificationsApi.list).toHaveBeenCalledTimes(2));
  });

  it('marks all notifications read', async () => {
    const client = createProductionQueryClient();
    client.setQueryData(notificationKeys.unreadCount, 4);
    render(withClient(client, <NotificationList />));
    await rowOf('Call Jane back');

    fireEvent.click(screen.getByRole('button', { name: 'Mark all read' }));

    await waitFor(() => expect(notificationsApi.markAllRead).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(mockShowSuccess).toHaveBeenCalledWith('Marked 4 notifications read'));
    await waitFor(() => expect(isInvalidated(client, notificationKeys.unreadCount)).toBe(true));
    await waitFor(() => expect(notificationsApi.list).toHaveBeenCalledTimes(2));
  });

  it('offers Open links to admin and sales but not to support', async () => {
    for (const role of ['admin', 'sales'] as const) {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));
      const view = render(<NotificationList />);

      const lead = await rowOf('Call Jane back');
      expect(within(lead).getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/leads/5');
      const deal = await rowOf('Deal moved');
      expect(within(deal).getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/deals/9');
      view.unmount();
    }

    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'support' })));
    render(<NotificationList />);
    await rowOf('Call Jane back');
    expect(screen.queryByRole('link', { name: 'Open' })).not.toBeInTheDocument();
    // Support still marks its notifications read.
    expect(screen.getByRole('button', { name: 'Mark read' })).toBeInTheDocument();
  });
});
