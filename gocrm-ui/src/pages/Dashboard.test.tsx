import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Dashboard } from './Dashboard';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

// recharts' ResponsiveContainer needs ResizeObserver, which jsdom lacks.
global.ResizeObserver = vi.fn().mockImplementation(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const mockGetStats = vi.fn();
const mockGetRecentActivities = vi.fn();
const mockGetSalesPerformance = vi.fn();
const mockGetUpcomingTasks = vi.fn();

vi.mock('@/api/endpoints', () => ({
  dashboardApi: {
    getStats: () => mockGetStats(),
    getRecentActivities: (limit?: number) => mockGetRecentActivities(limit),
    getSalesPerformance: (period?: string) => mockGetSalesPerformance(period),
    getUpcomingTasks: (limit?: number) => mockGetUpcomingTasks(limit),
  },
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

const renderDashboard = () => {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
      },
    },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Dashboard />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('Dashboard', () => {
  beforeEach(() => {
    mockUseAuth.mockReset();
    mockGetStats.mockReset();
    mockGetRecentActivities.mockReset();
    mockGetSalesPerformance.mockReset();
    mockGetUpcomingTasks.mockReset();
    mockGetStats.mockResolvedValue({
      total_leads: 10,
      total_customers: 4,
      open_tickets: 3,
      pending_tasks: 7,
      conversion_rate: 40,
    });
    mockGetRecentActivities.mockResolvedValue([
      {
        id: 'lead-1',
        type: 'lead_created',
        title: 'New lead created',
        description: 'Acme Corp added by admin@example.com',
        user: { id: 1, username: 'admin@example.com', first_name: 'Admin', last_name: 'User' },
        created_at: '2026-08-01T10:00:00Z',
      },
    ]);
    mockGetSalesPerformance.mockResolvedValue({
      labels: ['2026-07', '2026-08'],
      datasets: [{ label: 'Conversions', data: [3, 5] }],
    });
    mockGetUpcomingTasks.mockResolvedValue([
      {
        id: 42,
        title: 'Call the customer back',
        description: '',
        status: 'pending',
        priority: 'high',
        due_date: '2026-08-07T09:00:00Z',
        assigned_to: 1,
        created_by: 1,
        created_at: '2026-08-01T10:00:00Z',
        updated_at: '2026-08-01T10:00:00Z',
      },
    ]);
  });

  it.each(['admin', 'sales', 'support'] as const)(
    'fetches and renders the stats cards for the %s role',
    async (role) => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ role })));

      renderDashboard();

      expect(await screen.findByText('Total Leads')).toBeInTheDocument();
      expect(screen.getByText('Total Customers')).toBeInTheDocument();
      expect(screen.getByText('Open Tickets')).toBeInTheDocument();
      expect(screen.getByText('Pending Tasks')).toBeInTheDocument();
      expect(screen.getByText('Conversion Rate')).toBeInTheDocument();
      await waitFor(() => expect(mockGetStats).toHaveBeenCalled());
    }
  );

  it('does not fetch or render stats for the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'customer' })));

    renderDashboard();

    // The welcome heading still renders.
    expect(screen.getByText('Dashboard')).toBeInTheDocument();

    // No stats cards and no request to the forbidden endpoint.
    expect(screen.queryByText('Total Leads')).not.toBeInTheDocument();
    expect(screen.queryByText('Total Customers')).not.toBeInTheDocument();
    expect(screen.queryByText('Conversion Rate')).not.toBeInTheDocument();
    await waitFor(() => expect(mockGetStats).not.toHaveBeenCalled());
  });

  it('renders recent activities and upcoming tasks for the admin role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));

    renderDashboard();

    expect(await screen.findByText('Recent Activities')).toBeInTheDocument();
    expect(await screen.findByText('New lead created')).toBeInTheDocument();

    expect(screen.getByText('Upcoming Tasks')).toBeInTheDocument();
    expect(await screen.findByText('Call the customer back')).toBeInTheDocument();
    expect(screen.getByText('high')).toBeInTheDocument();

    expect(screen.getByText('Sales Performance')).toBeInTheDocument();

    await waitFor(() => {
      expect(mockGetRecentActivities).toHaveBeenCalledWith(10);
      expect(mockGetSalesPerformance).toHaveBeenCalledWith('month');
      expect(mockGetUpcomingTasks).toHaveBeenCalledWith(5);
    });
  });

  it('shows a quiet empty state when there are no activities or tasks', async () => {
    mockGetRecentActivities.mockResolvedValue([]);
    mockGetUpcomingTasks.mockResolvedValue([]);
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'sales' })));

    renderDashboard();

    expect(await screen.findByText('Recent Activities')).toBeInTheDocument();
    const emptyLines = await screen.findAllByText('Nothing yet');
    expect(emptyLines).toHaveLength(2);
  });

  it('calls no dashboard endpoints and shows no panels for the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'customer' })));

    renderDashboard();

    expect(screen.queryByText('Sales Performance')).not.toBeInTheDocument();
    expect(screen.queryByText('Recent Activities')).not.toBeInTheDocument();
    expect(screen.queryByText('Upcoming Tasks')).not.toBeInTheDocument();

    await waitFor(() => {
      expect(mockGetStats).not.toHaveBeenCalled();
      expect(mockGetRecentActivities).not.toHaveBeenCalled();
      expect(mockGetSalesPerformance).not.toHaveBeenCalled();
      expect(mockGetUpcomingTasks).not.toHaveBeenCalled();
    });
  });
});
