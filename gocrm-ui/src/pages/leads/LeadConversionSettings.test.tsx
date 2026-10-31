import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within, act } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { Component as LeadDetail } from './LeadDetail';
import { Component as LeadList } from './LeadList';
import { configurationsApi, leadsApi, type Configuration } from '@/api/endpoints';
import { createMockLead, createMockUser } from '@/test/factories';

// The lead pages under the real configuration provider, for a signed-in
// admin: what they offer while the conversion rules load, after the load
// failed, when the server left a rule out, and once they are known.

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', async () => {
  const configurations = await vi.importActual<typeof import('@/api/endpoints/configurations')>(
    '@/api/endpoints/configurations',
  );
  return {
    configurationsApi: { ...configurations.configurationsApi, getUIConfigurations: vi.fn() },
    leadsApi: { getLead: vi.fn(), getLeads: vi.fn(), deleteLead: vi.fn(), convertLead: vi.fn() },
    authApi: { getCurrentUser: vi.fn(), login: vi.fn(), register: vi.fn(), logout: vi.fn() },
  };
});

const signedIn = vi.hoisted(() => ({ user: null as unknown }));
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: signedIn.user,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

const entry = (key: string, value: string, type: Configuration['type']): Configuration => ({
  id: 0,
  key,
  value,
  type,
  category: 'leads',
  description: '',
  default_value: '',
  is_system: true,
  is_read_only: true,
  valid_values: '',
  created_at: '',
  updated_at: '',
});

const rules = (overrides: { statuses?: string; requireNotes?: string | null } = {}) =>
  [
    entry('leads.conversion.allowed_statuses', overrides.statuses ?? '["contacted"]', 'array'),
    overrides.requireNotes === null
      ? null
      : entry('leads.conversion.require_notes', overrides.requireNotes ?? 'true', 'boolean'),
    entry('leads.conversion.auto_assign_owner', 'false', 'boolean'),
  ].filter((config): config is Configuration => config !== null);

const never = () => new Promise<Configuration[]>(() => {});

describe('lead conversion under the configuration provider', () => {
  const contacted = createMockLead({ id: 1, company_name: 'Contacted Co', contact_name: 'Cara', status: 'contacted' });
  const fresh = createMockLead({ id: 2, company_name: 'New Co', contact_name: 'Ned', status: 'new' });
  const converted = createMockLead({ id: 3, company_name: 'Done Co', contact_name: 'Dot', status: 'converted' });

  beforeEach(() => {
    vi.clearAllMocks();
    signedIn.user = createMockUser({ id: 1, role: 'admin' });
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '1' });
    vi.mocked(leadsApi.getLead).mockResolvedValue(contacted);
    vi.mocked(leadsApi.getLeads).mockResolvedValue({
      data: [contacted, fresh, converted],
      total: 3,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  describe('lead page', () => {
    it('shows Convert disabled while the rules load, without guessing', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockImplementation(never);
      render(<LeadDetail />);

      const pending = await screen.findByTestId('convert-settings-pending');
      expect(pending).toHaveTextContent('Loading conversion settings');
      expect(within(pending).getByRole('button', { name: /Convert to Customer/i })).toBeDisabled();
    });

    it('offers a retry after a failed load and converts once the rules arrive', async () => {
      vi.mocked(configurationsApi.getUIConfigurations)
        .mockRejectedValueOnce(new Error('Server unavailable'))
        .mockResolvedValueOnce(rules());
      render(<LeadDetail />);

      const pending = await screen.findByTestId('convert-settings-pending');
      await waitFor(() => expect(pending).toHaveTextContent('Could not load conversion settings'));
      expect(within(pending).getByRole('button', { name: /Convert to Customer/i })).toBeDisabled();

      fireEvent.click(within(pending).getByRole('button', { name: 'Retry' }));
      await waitFor(() => expect(screen.queryByTestId('convert-settings-pending')).not.toBeInTheDocument());
      const convert = screen.getByRole('button', { name: /Convert to Customer/i });
      expect(convert).toBeEnabled();
      expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(2);

      // The notes rule from the server applies in the dialog.
      fireEvent.click(convert);
      const dialog = await screen.findByRole('dialog');
      expect(within(dialog).getByLabelText(/Conversion Notes/)).toBeRequired();
    });

    it('treats a missing notes rule as unknown, not as optional notes', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(rules({ requireNotes: null }));
      render(<LeadDetail />);

      const pending = await screen.findByTestId('convert-settings-pending');
      await waitFor(() => expect(pending).toHaveTextContent('Could not load conversion settings'));
      expect(within(pending).getByRole('button', { name: /Convert to Customer/i })).toBeDisabled();
    });

    it('follows the configured statuses once known', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(rules({ statuses: '["qualified"]' }));
      render(<LeadDetail />);

      await waitFor(() => expect(configurationsApi.getUIConfigurations).toHaveBeenCalled());
      await waitFor(() => expect(screen.queryByTestId('convert-settings-pending')).not.toBeInTheDocument());
      await screen.findByRole('button', { name: /Edit/i });
      expect(screen.queryByRole('button', { name: /Convert to Customer/i })).not.toBeInTheDocument();
    });
  });

  describe('lead list', () => {
    const openMenu = async (company: string) => {
      fireEvent.click(await screen.findByRole('button', { name: `More actions for ${company}` }));
      return screen.findByRole('menu');
    };

    it('shows the action disabled on every unconverted lead while the rules load', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockImplementation(never);
      render(<LeadList />);
      await screen.findByText('New Co');

      const menu = await openMenu('New Co');
      expect(within(menu).getByRole('menuitem', { name: /Convert to Customer/ })).toHaveAttribute(
        'aria-disabled',
        'true',
      );
      expect(screen.queryByRole('button', { name: 'More actions for Done Co' })).not.toBeInTheDocument();
    });

    it('reports a failed load with a retry, then follows the rules', async () => {
      vi.mocked(configurationsApi.getUIConfigurations)
        .mockRejectedValueOnce(new Error('Server unavailable'))
        .mockResolvedValueOnce(rules());
      render(<LeadList />);

      const alert = await screen.findByTestId('conversion-settings-unavailable');
      const menu = await openMenu('Contacted Co');
      expect(within(menu).getByRole('menuitem', { name: /Convert to Customer/ })).toHaveAttribute(
        'aria-disabled',
        'true',
      );
      fireEvent.keyDown(menu, { key: 'Escape' });

      await act(async () => {
        fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
      });
      await waitFor(() =>
        expect(screen.queryByTestId('conversion-settings-unavailable')).not.toBeInTheDocument(),
      );
      // Contacted is allowed, New is not.
      expect(screen.queryByRole('button', { name: 'More actions for New Co' })).not.toBeInTheDocument();
      const ready = await openMenu('Contacted Co');
      expect(within(ready).getByRole('menuitem', { name: 'Convert to Customer' })).not.toHaveAttribute(
        'aria-disabled',
      );
    });

    it('treats missing allowed statuses as unknown', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(
        rules().filter((config) => config.key !== 'leads.conversion.allowed_statuses'),
      );
      render(<LeadList />);

      expect(await screen.findByTestId('conversion-settings-unavailable')).toBeInTheDocument();
    });
  });
});
