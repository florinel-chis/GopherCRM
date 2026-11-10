import { describe, it, expect, vi, beforeEach, afterEach, type Mock, type MockInstance } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as LeadList } from './LeadList';
import { leadsApi } from '@/api/endpoints';
import { createMockLead, createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  leadsApi: {
    getLeads: vi.fn(),
    deleteLead: vi.fn(),
    convertLead: vi.fn(),
    exportLeads: vi.fn(),
  },
}));

vi.mock('@/hooks/useConfiguration', () => ({
  useConfiguration: () => ({
    getLeadConversion: () => ({
      status: 'ready',
      settings: { allowedStatuses: ['qualified'], requireNotes: false, autoAssignOwner: false },
    }),
    refreshConfigurations: async () => {},
  }),
}));

const ADMIN_LABEL = 'Export all leads (CSV)';
const SALES_LABEL = 'Export my leads (CSV)';
const ERROR_TEXT = 'Could not export leads.';
const OBJECT_URL = 'blob:http://localhost/lead-export';

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

interface ClickedAnchor {
  anchor: HTMLAnchorElement;
  connected: boolean;
  href: string;
  download: string;
}

const renderList = async () => {
  render(<LeadList />);
  await screen.findByText('Alpha Leads');
};

// An error shaped like the one axios rejects with for responseType 'blob':
// the JSON envelope arrives as a Blob the page does not parse.
const axiosBlobError = (status: number) =>
  Object.assign(new Error(`Request failed with status code ${status}`), {
    isAxiosError: true,
    response: {
      status,
      data: new Blob([JSON.stringify({ success: false, error: { message: 'nope' } })], {
        type: 'application/json',
      }),
    },
  });

describe('LeadList CSV export', () => {
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;
  let createObjectURL: Mock<(obj: Blob | MediaSource) => string>;
  let revokeObjectURL: Mock<(url: string) => void>;
  let anchorClick: MockInstance<() => void>;
  let clicked: ClickedAnchor[];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(leadsApi.getLeads).mockResolvedValue({
      data: [createMockLead({ id: 1, company_name: 'Alpha Leads', status: 'new' })],
      total: 1,
      page: 1,
      limit: 10,
      total_pages: 1,
    });

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
        anchor: this,
        connected: this.isConnected,
        href: this.getAttribute('href') ?? '',
        download: this.getAttribute('download') ?? '',
      });
    });
  });

  afterEach(() => {
    anchorClick.mockRestore();
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
  });

  it('shows the Export all leads (CSV) button to an admin', async () => {
    signIn('admin');
    await renderList();

    expect(screen.getByRole('button', { name: ADMIN_LABEL })).toBeEnabled();
    expect(screen.queryByRole('button', { name: SALES_LABEL })).not.toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /export/i })).toHaveLength(1);
  });

  it('shows the Export my leads (CSV) button to a sales user', async () => {
    signIn('sales');
    await renderList();

    expect(screen.getByRole('button', { name: SALES_LABEL })).toBeEnabled();
    expect(screen.queryByRole('button', { name: ADMIN_LABEL })).not.toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /export/i })).toHaveLength(1);
  });

  it('hides the export button from a support user', async () => {
    signIn('support');
    await renderList();

    expect(screen.queryByRole('button', { name: /export/i })).not.toBeInTheDocument();
    expect(screen.queryByText(ADMIN_LABEL)).not.toBeInTheDocument();
    expect(screen.queryByText(SALES_LABEL)).not.toBeInTheDocument();
    expect(leadsApi.exportLeads).not.toHaveBeenCalled();
  });

  it('hides the export button from a customer user', async () => {
    signIn('customer');
    await renderList();

    expect(screen.queryByRole('button', { name: /export/i })).not.toBeInTheDocument();
    expect(screen.queryByText(ADMIN_LABEL)).not.toBeInTheDocument();
    expect(screen.queryByText(SALES_LABEL)).not.toBeInTheDocument();
    expect(leadsApi.exportLeads).not.toHaveBeenCalled();
  });

  it('downloads the export as leads-export.csv when clicked', async () => {
    signIn('admin');
    const blob = new Blob(['id,email\n1,a@example.com\n'], { type: 'text/csv' });
    vi.mocked(leadsApi.exportLeads).mockResolvedValue(blob);
    await renderList();

    fireEvent.click(screen.getByRole('button', { name: ADMIN_LABEL }));

    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(leadsApi.exportLeads).toHaveBeenCalledTimes(1);
    expect(vi.mocked(leadsApi.exportLeads).mock.calls[0]).toEqual([]);
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(createObjectURL).toHaveBeenCalledWith(blob);

    const [download] = clicked;
    expect(download.connected).toBe(true);
    expect(download.href).toBe(OBJECT_URL);
    expect(download.download).toBe('leads-export.csv');
    expect(download.anchor.isConnected).toBe(false);
    expect(document.body.contains(download.anchor)).toBe(false);

    // The URL is revoked on a later tick, once the download has started.
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL));
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: ADMIN_LABEL })).toBeEnabled(),
    );
    expect(screen.queryByText(ERROR_TEXT)).not.toBeInTheDocument();
  });

  it('ignores the list filters when exporting', async () => {
    signIn('sales');
    vi.mocked(leadsApi.exportLeads).mockResolvedValue(new Blob(['id\n'], { type: 'text/csv' }));
    await renderList();

    fireEvent.change(screen.getByPlaceholderText('Search leads...'), {
      target: { value: 'Alpha' },
    });
    const chooseOption = async (select: HTMLElement, option: string) => {
      fireEvent.mouseDown(select);
      const listbox = await screen.findByRole('listbox');
      fireEvent.click(within(listbox).getByRole('option', { name: option }));
      await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
    };
    const [statusSelect, classificationSelect] = screen.getAllByRole('combobox');
    await chooseOption(statusSelect, 'Qualified');
    await chooseOption(classificationSelect, 'Hot Lead');

    // The list really is filtered now.
    await waitFor(() =>
      expect(leadsApi.getLeads).toHaveBeenLastCalledWith(
        expect.objectContaining({
          search: 'Alpha',
          status: 'qualified',
          classification: 'hot_lead',
        }),
      ),
    );

    fireEvent.click(await screen.findByRole('button', { name: SALES_LABEL }));

    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(leadsApi.exportLeads).toHaveBeenCalledTimes(1);
    expect(vi.mocked(leadsApi.exportLeads).mock.calls[0]).toHaveLength(0);
    expect(clicked[0].download).toBe('leads-export.csv');
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL));
  });

  it('disables the button while the export is in flight', async () => {
    signIn('admin');
    let resolveExport: (blob: Blob) => void = () => {};
    vi.mocked(leadsApi.exportLeads).mockImplementation(
      () =>
        new Promise<Blob>((resolve) => {
          resolveExport = resolve;
        }),
    );
    await renderList();

    const button = screen.getByRole('button', { name: ADMIN_LABEL });
    expect(button).toBeEnabled();
    fireEvent.click(button);

    await waitFor(() => expect(screen.getByRole('button', { name: ADMIN_LABEL })).toBeDisabled());
    expect(leadsApi.exportLeads).toHaveBeenCalledTimes(1);
    expect(clicked).toHaveLength(0);

    resolveExport(new Blob(['id\n'], { type: 'text/csv' }));

    await waitFor(() => expect(screen.getByRole('button', { name: ADMIN_LABEL })).toBeEnabled());
    expect(clicked).toHaveLength(1);
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL));
  });

  for (const status of [403, 500, 429]) {
    it(`shows an error and saves no file when the export answers ${status}`, async () => {
      signIn('admin');
      vi.mocked(leadsApi.exportLeads).mockRejectedValue(axiosBlobError(status));
      await renderList();

      fireEvent.click(screen.getByRole('button', { name: ADMIN_LABEL }));

      expect(await screen.findByText(ERROR_TEXT)).toBeInTheDocument();
      expect(leadsApi.exportLeads).toHaveBeenCalledTimes(1);
      expect(createObjectURL).not.toHaveBeenCalled();
      expect(revokeObjectURL).not.toHaveBeenCalled();
      expect(clicked).toHaveLength(0);
      expect(document.querySelector('a[download]')).toBeNull();
      await waitFor(() =>
        expect(screen.getByRole('button', { name: ADMIN_LABEL })).toBeEnabled(),
      );
    });
  }
});
