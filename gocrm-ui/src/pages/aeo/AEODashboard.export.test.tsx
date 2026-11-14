import { describe, it, expect, vi, beforeEach, afterEach, type Mock, type MockInstance } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Component as AEODashboard } from './AEODashboard';
import { aeoApi, type AEODashboard as AEODashboardData } from '@/api/endpoints/aeo';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

// recharts' ResponsiveContainer needs ResizeObserver, which jsdom lacks.
global.ResizeObserver = vi.fn().mockImplementation(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

const mockUseAuth = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/aeo', () => ({
  aeoApi: {
    getDashboard: vi.fn(),
    exportResults: vi.fn(),
  },
}));

const OBJECT_URL = 'blob:http://localhost/aeo-export';

const dashboard: AEODashboardData = {
  from: '2026-11-08',
  to: '2026-11-14',
  days: 7,
  total_answers: 0,
  failed_answers: 0,
  brand_mentions: 0,
  visibility: 0,
  by_provider: [],
  timeline: [],
  share_of_voice: [],
  competitor_timeline: [],
};

const signIn = (role: User['role']) => {
  mockUseAuth.mockReturnValue({
    user: createMockUser({ role }),
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });
};

const renderPage = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <AEODashboard />
    </QueryClientProvider>,
  );
};

// An error shaped like the one axios rejects with for responseType 'blob'.
const axiosBlobError = (status: number, body: unknown) =>
  Object.assign(new Error(`Request failed with status code ${status}`), {
    isAxiosError: true,
    response: {
      status,
      data: new Blob([JSON.stringify(body)], { type: 'application/json' }),
    },
  });

interface ClickedAnchor {
  connected: boolean;
  href: string;
  download: string;
}

describe('AEODashboard export', () => {
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;
  let createObjectURL: Mock<(obj: Blob | MediaSource) => string>;
  let revokeObjectURL: Mock<(url: string) => void>;
  let anchorClick: MockInstance<() => void>;
  let clicked: ClickedAnchor[];

  beforeEach(() => {
    vi.clearAllMocks();
    signIn('admin');
    vi.mocked(aeoApi.getDashboard).mockResolvedValue(dashboard);

    // jsdom implements neither, and navigating to a blob URL is not supported.
    createObjectURL = vi.fn((_obj: Blob | MediaSource) => OBJECT_URL);
    revokeObjectURL = vi.fn((_url: string) => {});
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    clicked = [];
    anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      clicked.push({
        connected: this.isConnected && document.body.contains(this),
        href: this.getAttribute('href') ?? '',
        download: this.getAttribute('download') ?? '',
      });
    });
  });

  afterEach(() => {
    anchorClick.mockRestore();
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
    vi.useRealTimers();
  });

  it('shows CSV and JSON export controls to admin, sales and support', async () => {
    for (const role of ['admin', 'sales', 'support'] as const) {
      signIn(role);
      const { unmount } = renderPage();
      expect(await screen.findByRole('button', { name: 'Export CSV' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Export JSON' })).toBeEnabled();
      unmount();
    }
  });

  it('hides the export controls from a customer', async () => {
    signIn('customer');
    renderPage();

    expect(await screen.findByText('No AEO data yet')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Export CSV' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Export JSON' })).not.toBeInTheDocument();
  });

  it('exports the selected days window and saves the file', async () => {
    // Only Date is faked, so Testing Library's waits and the deferred revoke
    // still run on real timers.
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-11-14T23:30:00Z'));
    const csv = new Blob(['run_id\n'], { type: 'text/csv' });
    const json = new Blob(['{}'], { type: 'application/json' });
    vi.mocked(aeoApi.exportResults).mockResolvedValueOnce(csv).mockResolvedValueOnce(json);

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Last 7 days' }));
    await waitFor(() => expect(aeoApi.getDashboard).toHaveBeenCalledWith(7));

    fireEvent.click(screen.getByRole('button', { name: 'Export CSV' }));
    await waitFor(() => expect(clicked).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Export JSON' })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: 'Export JSON' }));
    await waitFor(() => expect(clicked).toHaveLength(2));

    const calls = vi.mocked(aeoApi.exportResults).mock.calls.map(([params]) => params);
    expect(calls).toStrictEqual([
      { format: 'csv', days: 7 },
      { format: 'json', days: 7 },
    ]);
    calls.forEach((params) => expect('run_id' in params).toBe(false));

    expect(createObjectURL).toHaveBeenNthCalledWith(1, csv);
    expect(createObjectURL).toHaveBeenNthCalledWith(2, json);
    expect(clicked).toStrictEqual([
      { connected: true, href: OBJECT_URL, download: 'aeo-export-7d-2026-11-14.csv' },
      { connected: true, href: OBJECT_URL, download: 'aeo-export-7d-2026-11-14.json' },
    ]);
    expect(document.querySelector('a[download]')).toBeNull();

    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledTimes(2));
    expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL);
    expect(showError).not.toHaveBeenCalled();
  });

  it('shows the too-large error and saves no file', async () => {
    vi.mocked(aeoApi.exportResults).mockRejectedValue(
      axiosBlobError(422, {
        success: false,
        error: {
          code: 'EXPORT_TOO_LARGE',
          message: 'export too large',
          details: { max_answers: 10000 },
        },
      }),
    );

    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Export CSV' }));

    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        'This export holds more than 10,000 answers. Choose a shorter window or a single run.',
      ),
    );
    expect(aeoApi.exportResults).toHaveBeenCalledWith({ format: 'csv', days: 30 });
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(revokeObjectURL).not.toHaveBeenCalled();
    expect(clicked).toHaveLength(0);
    expect(document.querySelector('a[download]')).toBeNull();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Export CSV' })).toBeEnabled());
  });
});
