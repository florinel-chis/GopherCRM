import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Dashboard } from './Dashboard';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';
import { formatDealAmount } from './deals/dealFormat';

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
const mockGetPipeline = vi.fn();
const mockGetUIConfigurations = vi.fn();

vi.mock('@/api/endpoints', () => ({
  dashboardApi: {
    getStats: () => mockGetStats(),
    getRecentActivities: (limit?: number) => mockGetRecentActivities(limit),
    getSalesPerformance: (period?: string) => mockGetSalesPerformance(period),
    getUpcomingTasks: (limit?: number) => mockGetUpcomingTasks(limit),
    getPipeline: () => mockGetPipeline(),
  },
  configurationsApi: {
    getUIConfigurations: () => mockGetUIConfigurations(),
  },
}));

const pipelineResponse = {
  stages: [
    {
      stage: 'qualification',
      count: 2,
      totals: [
        { currency: 'EUR', amount_cents: 300000, weighted_cents: 30000 },
        { currency: 'USD', amount_cents: 80000, weighted_cents: 8000 },
      ],
    },
    { stage: 'proposal', count: 1, totals: [{ currency: 'EUR', amount_cents: 100000, weighted_cents: 40000 }] },
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
};

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
    mockGetPipeline.mockReset();
    mockGetUIConfigurations.mockReset();
    mockGetPipeline.mockResolvedValue(pipelineResponse);
    mockGetUIConfigurations.mockResolvedValue([{ key: 'deals.default_currency', value: 'EUR' }]);
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

  describe('deal widgets', () => {
    it.each(['admin', 'sales'] as const)(
      'shows the pipeline chart and the won-this-month tile for the %s role',
      async (role) => {
        mockUseAuth.mockReturnValue(authState(createMockUser({ role })));

        renderDashboard();

        expect(await screen.findByRole('heading', { name: 'Pipeline by stage' })).toBeInTheDocument();
        expect(await screen.findByTestId('pipeline-default-total')).toHaveTextContent(
          `Open pipeline in EUR: ${formatDealAmount(400000, 'EUR')}`
        );
        // The USD deal is listed apart, never added to the EUR bars.
        const others = screen.getByRole('table', { name: 'Other currencies' });
        expect(within(others).getByRole('rowheader', { name: 'USD' })).toBeInTheDocument();

        const tile = screen.getByRole('region', { name: 'Won this month' });
        expect(within(tile).getByTestId('won-this-month-count')).toHaveTextContent('2');
        expect(within(tile).getByText(formatDealAmount(400000, 'EUR'))).toBeInTheDocument();
        expect(within(tile).getByText(formatDealAmount(100000, 'USD'))).toBeInTheDocument();

        expect(screen.getByRole('link', { name: 'Open the board' })).toHaveAttribute('href', '/deals/board');
        expect(screen.getByRole('link', { name: 'View the board' })).toHaveAttribute('href', '/deals/board');
        expect(mockGetPipeline).toHaveBeenCalledTimes(1);
      }
    );

    it('draws the chart in the configured default currency', async () => {
      mockGetUIConfigurations.mockResolvedValue([{ key: 'deals.default_currency', value: 'usd' }]);
      mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));

      renderDashboard();

      await waitFor(() =>
        expect(screen.getByTestId('pipeline-default-total')).toHaveTextContent(
          `Open pipeline in USD: ${formatDealAmount(80000, 'USD')}`
        )
      );
      const others = screen.getByRole('table', { name: 'Other currencies' });
      expect(within(others).getByRole('rowheader', { name: 'EUR' })).toBeInTheDocument();
    });

    it('shows the empty states when there are no deals', async () => {
      mockGetPipeline.mockResolvedValue({
        stages: ['qualification', 'proposal', 'negotiation', 'won', 'lost'].map((stage) => ({
          stage,
          count: 0,
          totals: [],
        })),
        won_this_month: { count: 0, totals: [] },
      });
      mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'sales' })));

      renderDashboard();

      expect(await screen.findByText('No open deals yet')).toBeInTheDocument();
      expect(screen.getByText('No deals won yet this month')).toBeInTheDocument();
      expect(screen.getByTestId('won-this-month-count')).toHaveTextContent('0');
    });

    it.each(['support', 'customer'] as const)(
      'shows no deal widgets and makes no pipeline request for the %s role',
      async (role) => {
        mockUseAuth.mockReturnValue(authState(createMockUser({ role })));

        renderDashboard();

        expect(screen.getByText('Dashboard')).toBeInTheDocument();
        if (role === 'support') {
          // The rest of the dashboard still loads for support.
          expect(await screen.findByText('Total Leads')).toBeInTheDocument();
        }
        expect(screen.queryByText('Pipeline by stage')).not.toBeInTheDocument();
        expect(screen.queryByText('Won this month')).not.toBeInTheDocument();
        await waitFor(() => {
          expect(mockGetPipeline).not.toHaveBeenCalled();
          expect(mockGetUIConfigurations).not.toHaveBeenCalled();
        });
      }
    );
  });
});
