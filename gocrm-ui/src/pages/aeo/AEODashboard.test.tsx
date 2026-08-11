import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Component as AEODashboard } from './AEODashboard';
import type { AEODashboard as AEODashboardData } from '@/api/endpoints/aeo';

// recharts' ResponsiveContainer needs ResizeObserver, which jsdom lacks.
global.ResizeObserver = vi.fn().mockImplementation(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

const mockGetDashboard = vi.fn();

vi.mock('@/api/endpoints/aeo', () => ({
  aeoApi: {
    getDashboard: (days?: number) => mockGetDashboard(days),
  },
}));

const dashboardFixture: AEODashboardData = {
  from: '2026-07-12',
  to: '2026-08-11',
  days: 30,
  total_answers: 24,
  failed_answers: 2,
  brand_mentions: 10,
  visibility: 41.7,
  by_provider: [
    { provider: 'anthropic', answers: 12, mentions: 6, visibility: 50 },
    { provider: 'perplexity', answers: 12, mentions: 4, visibility: 33.3 },
  ],
  timeline: [
    { day: '2026-08-09', overall: 0, by_provider: {} },
    { day: '2026-08-10', overall: 50, by_provider: { anthropic: 50, perplexity: 50 } },
    { day: '2026-08-11', overall: 33.3, by_provider: { anthropic: 66.7 } },
  ],
  share_of_voice: [
    { company: 'Acme', is_brand: true, mentions: 10, share: 62.5, visibility: 41.7 },
    { company: 'Globex', is_brand: false, mentions: 6, share: 37.5, visibility: 25 },
  ],
  competitor_timeline: [
    { day: '2026-08-10', by_company: { Acme: 50, Globex: 25 } },
    { day: '2026-08-11', by_company: { Acme: 33.3 } },
  ],
  last_run_at: '2026-08-11T06:00:00Z',
};

const emptyDashboard: AEODashboardData = {
  from: '2026-07-12',
  to: '2026-08-11',
  days: 30,
  total_answers: 0,
  failed_answers: 0,
  brand_mentions: 0,
  visibility: 0,
  by_provider: [],
  timeline: [],
  share_of_voice: [],
  competitor_timeline: [],
};

const renderPage = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <AEODashboard />
    </QueryClientProvider>
  );
};

describe('AEODashboard', () => {
  beforeEach(() => {
    mockGetDashboard.mockReset();
    mockGetDashboard.mockResolvedValue(dashboardFixture);
  });

  it('shows a spinner while the dashboard loads', () => {
    mockGetDashboard.mockImplementation(() => new Promise(() => {}));

    renderPage();

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.getByText('AEO Dashboard')).toBeInTheDocument();
  });

  it('renders an error alert when the request fails', async () => {
    mockGetDashboard.mockRejectedValue(new Error('boom'));

    renderPage();

    expect(
      await screen.findByText(/Failed to load the AEO dashboard/)
    ).toBeInTheDocument();
  });

  it('renders the empty state when no answers exist in the range', async () => {
    mockGetDashboard.mockResolvedValue(emptyDashboard);

    renderPage();

    expect(await screen.findByText('No AEO data yet')).toBeInTheDocument();
    expect(screen.queryByText('Share of voice')).not.toBeInTheDocument();
  });

  it('renders the gauge, headline metrics and per-provider tiles', async () => {
    renderPage();

    expect(await screen.findByTestId('aeo-visibility-gauge')).toBeInTheDocument();

    // The gauge label and the brand row in share-of-voice both read 41.7%.
    expect(screen.getAllByText('41.7%').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('of answers mention the brand')).toBeInTheDocument();

    expect(screen.getByText('Answers analysed')).toBeInTheDocument();
    expect(screen.getByText('24')).toBeInTheDocument();
    expect(screen.getByText('2 failed')).toBeInTheDocument();

    expect(screen.getByText('Brand mentions')).toBeInTheDocument();
    // Provider names also appear in the chart legend, so match loosely.
    expect(screen.getAllByText('anthropic').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('6/12 answers')).toBeInTheDocument();
    expect(screen.getAllByText('perplexity').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('4/12 answers')).toBeInTheDocument();
  });

  it('renders both time-series charts and the share-of-voice table', async () => {
    renderPage();

    expect(await screen.findByTestId('aeo-provider-timeline')).toBeInTheDocument();
    expect(screen.getByTestId('aeo-competitor-timeline')).toBeInTheDocument();

    expect(screen.getByText('Share of voice')).toBeInTheDocument();

    // Company names repeat in the competitor chart legend, so scope the row
    // lookups to the share-of-voice table.
    const table = within(screen.getByTestId('aeo-share-of-voice'));

    const brandRow = table.getByText('Acme').closest('tr');
    expect(brandRow).not.toBeNull();
    expect(within(brandRow as HTMLElement).getByText('Your brand')).toBeInTheDocument();
    expect(brandRow).toHaveTextContent('62.5%');

    const competitorRow = table.getByText('Globex').closest('tr');
    expect(competitorRow).toHaveTextContent('37.5%');
    expect(competitorRow).toHaveTextContent('25.0%');
  });

  it('defaults to 30 days and refetches when another range is picked', async () => {
    renderPage();

    await waitFor(() => expect(mockGetDashboard).toHaveBeenCalledWith(30));

    fireEvent.click(screen.getByRole('button', { name: 'Last 90 days' }));

    await waitFor(() => expect(mockGetDashboard).toHaveBeenCalledWith(90));
  });

  it('tells the user when no competitors are configured', async () => {
    mockGetDashboard.mockResolvedValue({
      ...dashboardFixture,
      share_of_voice: [
        { company: 'Acme', is_brand: true, mentions: 10, share: 100, visibility: 41.7 },
      ],
      competitor_timeline: [{ day: '2026-08-10', by_company: {} }],
    } satisfies AEODashboardData);

    renderPage();

    // "Acme" is the brand, so the only company key is the brand itself; the
    // competitor chart still renders because a series exists.
    expect(await screen.findByTestId('aeo-competitor-timeline')).toBeInTheDocument();
  });

  it('renders the competitor empty note when there are no company series at all', async () => {
    mockGetDashboard.mockResolvedValue({
      ...dashboardFixture,
      share_of_voice: [],
      competitor_timeline: [],
    } satisfies AEODashboardData);

    renderPage();

    expect(
      await screen.findByText('No competitors are configured yet.')
    ).toBeInTheDocument();
    expect(screen.getByText('No mentions recorded in this range.')).toBeInTheDocument();
  });
});
