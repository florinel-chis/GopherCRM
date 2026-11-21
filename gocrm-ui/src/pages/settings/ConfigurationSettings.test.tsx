import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { AxiosError, AxiosHeaders } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as ConfigurationSettings } from './ConfigurationSettings';
import { configurationsApi } from '@/api/endpoints/configurations';
import type { Configuration } from '@/api/endpoints/configurations';
import type { Permission } from '@/types';
import { createMockUser } from '@/test/factories';
import { customPermissions, permissionsForRole } from '@/test/permissions';

const showSuccess = vi.fn();
const showError = vi.fn();
const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const signInWith = (permissions: Permission[]) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: 1, role: 'admin' }),
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

// getValue is the real parser, so json/array/number rows are seeded the way
// production seeds them.
vi.mock('@/api/endpoints/configurations', async () => {
  const actual = await vi.importActual<typeof import('@/api/endpoints/configurations')>(
    '@/api/endpoints/configurations',
  );
  return {
    configurationsApi: {
      getAll: vi.fn(),
      getUIConfigurations: vi.fn(),
      set: vi.fn(),
      reset: vi.fn(),
      getValue: actual.configurationsApi.getValue,
    },
  };
});

const apiError = (status: number, message: string): AxiosError => {
  const error = new AxiosError('request failed');
  error.response = {
    status,
    statusText: '',
    data: { message },
    headers: {},
    config: { headers: new AxiosHeaders() },
  };
  return error;
};

// The app-wide copy of GET /configurations/ui that pages such as the lead
// conversion dialog follow; a save or reset here must refresh it.
const refreshConfigurations = vi.hoisted(() => vi.fn(async () => {}));
vi.mock('@/hooks/useConfiguration', () => ({
  useConfiguration: () => ({ refreshConfigurations }),
}));

const baseConfig = (overrides: Partial<Configuration>): Configuration => ({
  id: 1,
  key: 'general.company_name',
  value: 'GopherCRM',
  type: 'string',
  category: 'general',
  description: 'Company name',
  default_value: 'GoCRM',
  is_system: false,
  is_read_only: false,
  valid_values: '',
  created_at: '2026-08-12T00:00:00Z',
  updated_at: '2026-08-12T00:00:00Z',
  ...overrides,
});

const sensitive = baseConfig({
  id: 2,
  key: 'integration.aeo.gemini_api_key',
  value: '',
  category: 'integration',
  description: 'Gemini API key',
  is_system: true,
  is_sensitive: true,
  is_set: true,
});

const dealCurrency = baseConfig({
  id: 3,
  key: 'deals.default_currency',
  value: 'EUR',
  category: 'deals',
  description: 'ISO 4217 currency code a new deal takes when none is sent',
  default_value: 'EUR',
  is_system: true,
});

const openIntegrationTab = async () => {
  const tab = await screen.findByRole('tab', { name: /Integration/ });
  fireEvent.click(tab);
};

describe('ConfigurationSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    signInWith(permissionsForRole('admin'));
    vi.mocked(configurationsApi.getAll).mockResolvedValue([baseConfig({}), sensitive]);
    vi.mocked(configurationsApi.set).mockResolvedValue(sensitive);
  });

  it('shows a configured chip instead of a value for a sensitive row', async () => {
    render(<ConfigurationSettings />);

    await openIntegrationTab();

    const chip = await screen.findByTestId('sensitive-chip-integration.aeo.gemini_api_key');
    expect(chip).toHaveTextContent('Configured');
  });

  it('marks an unset sensitive row as not configured', async () => {
    vi.mocked(configurationsApi.getAll).mockResolvedValue([
      { ...sensitive, is_set: false },
    ]);

    render(<ConfigurationSettings />);

    await openIntegrationTab();

    const chip = await screen.findByTestId('sensitive-chip-integration.aeo.gemini_api_key');
    expect(chip).toHaveTextContent('Not configured');
  });

  it('edits a sensitive row through an empty password field', async () => {
    render(<ConfigurationSettings />);

    await openIntegrationTab();

    fireEvent.click(await screen.findByTitle('Edit configuration'));

    const input = await screen.findByLabelText('New value');
    expect(input).toHaveAttribute('type', 'password');
    expect(input).toHaveAttribute('autocomplete', 'new-password');
    expect(input).toHaveValue('');

    fireEvent.change(input, { target: { value: 'fresh-secret' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(configurationsApi.set).toHaveBeenCalledWith('integration.aeo.gemini_api_key', {
        value: 'fresh-secret',
      });
    });
  });

  it('lists a deals configuration under the Deals tab, after Tasks, and edits it', async () => {
    vi.mocked(configurationsApi.getAll).mockResolvedValue([baseConfig({}), dealCurrency]);
    vi.mocked(configurationsApi.set).mockResolvedValue({ ...dealCurrency, value: 'USD' });

    render(<ConfigurationSettings />);

    const tabs = await screen.findAllByRole('tab');
    const labels = tabs.map((tab) => tab.textContent ?? '');
    expect(labels.indexOf('Deals (1)')).toBe(labels.indexOf('Tasks (0)') + 1);

    fireEvent.click(screen.getByRole('tab', { name: /^Deals/ }));

    expect(await screen.findByText('deals.default_currency')).toBeInTheDocument();
    expect(screen.queryByText('general.company_name')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTitle('Edit configuration'));

    const input = await screen.findByDisplayValue('EUR');
    fireEvent.change(input, { target: { value: 'USD' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(configurationsApi.set).toHaveBeenCalledWith('deals.default_currency', { value: 'USD' });
    });
  });

  it('refreshes the app-wide configuration after a save', async () => {
    vi.mocked(configurationsApi.set).mockResolvedValue(baseConfig({ value: 'Acme CRM' }));
    render(<ConfigurationSettings />);

    fireEvent.click(await screen.findByTitle('Edit configuration'));
    fireEvent.change(await screen.findByDisplayValue('GopherCRM'), { target: { value: 'Acme CRM' } });
    expect(refreshConfigurations).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(refreshConfigurations).toHaveBeenCalledTimes(1));
    expect(showSuccess).toHaveBeenCalledWith('Configuration updated successfully');
  });

  it('refreshes the app-wide configuration after a reset', async () => {
    vi.mocked(configurationsApi.reset).mockResolvedValue(baseConfig({}));
    render(<ConfigurationSettings />);

    fireEvent.click(await screen.findByTitle('Reset to default'));
    fireEvent.click(await screen.findByRole('button', { name: 'Reset' }));

    await waitFor(() => expect(refreshConfigurations).toHaveBeenCalledTimes(1));
  });

  it('does not refresh when the save fails', async () => {
    vi.mocked(configurationsApi.set).mockRejectedValue(new Error('nope'));
    render(<ConfigurationSettings />);

    fireEvent.click(await screen.findByTitle('Edit configuration'));
    fireEvent.click(await screen.findByRole('button', { name: 'Save' }));

    await waitFor(() => expect(showError).toHaveBeenCalledWith('Failed to update configuration'));
    expect(refreshConfigurations).not.toHaveBeenCalled();
  });

  it('keeps the plain editor for a non-sensitive row', async () => {
    render(<ConfigurationSettings />);

    fireEvent.click(await screen.findByTitle('Edit configuration'));

    await waitFor(() => {
      expect(screen.getByDisplayValue('GopherCRM')).toBeInTheDocument();
    });
    expect(screen.queryByLabelText('New value')).not.toBeInTheDocument();
  });

  describe('permissions', () => {
    it('shows no Edit or Reset to a caller who may only view settings', async () => {
      signInWith(customPermissions('settings.view'));
      render(<ConfigurationSettings />);

      expect(await screen.findByText('general.company_name')).toBeInTheDocument();
      expect(screen.queryByTitle('Edit configuration')).not.toBeInTheDocument();
      expect(screen.queryByTitle('Reset to default')).not.toBeInTheDocument();
      expect(screen.getByRole('columnheader', { name: 'Actions' })).toBeInTheDocument();
    });

    it('shows Edit and Reset to an admin on a writable row', async () => {
      render(<ConfigurationSettings />);

      expect(await screen.findByRole('button', { name: 'Edit general.company_name' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Reset general.company_name' })).toBeEnabled();
      expect(screen.getByTitle('Edit configuration')).toBeInTheDocument();
      expect(screen.getByTitle('Reset to default')).toBeInTheDocument();
    });

    it('shows the server message when a save is refused', async () => {
      vi.mocked(configurationsApi.set).mockRejectedValue(apiError(403, 'Insufficient permissions'));
      render(<ConfigurationSettings />);

      fireEvent.click(await screen.findByTitle('Edit configuration'));
      fireEvent.click(await screen.findByRole('button', { name: 'Save' }));

      await waitFor(() => expect(showError).toHaveBeenCalledWith('Insufficient permissions'));
      expect(showError).not.toHaveBeenCalledWith('Failed to update configuration');
      expect(screen.getByRole('dialog', { name: /Edit Configuration/ })).toBeInTheDocument();
    });
  });

  describe('typed values', () => {
    const jsonConfig = baseConfig({ id: 10, key: 'general.extra', type: 'json', value: '{}', default_value: '{}' });
    const arrayConfig = baseConfig({ id: 11, key: 'general.tags', type: 'array', value: '[]', default_value: '[]' });
    const intConfig = baseConfig({ id: 12, key: 'general.page_size', type: 'integer', value: '20', default_value: '20' });

    // Each case lists one row; the field is found by role so a missing label
    // is reported by the closing label assertion, not by the lookup.
    const openEditor = async (role: 'textbox' | 'spinbutton' = 'textbox') => {
      fireEvent.click(await screen.findByTitle('Edit configuration'));
      const dialog = await screen.findByRole('dialog', { name: /Edit Configuration/ });
      return within(dialog).getByRole(role);
    };

    it('takes a JSON value typed one key at a time and saves the parsed object', async () => {
      const user = userEvent.setup();
      vi.mocked(configurationsApi.getAll).mockResolvedValue([jsonConfig]);
      vi.mocked(configurationsApi.set).mockResolvedValue({ ...jsonConfig, value: '{"a":1}' });
      render(<ConfigurationSettings />);

      const field = await openEditor();
      expect(field).toHaveValue('{}');
      await user.clear(field);
      await user.type(field, '{{"a": 1}');
      expect(field).toHaveValue('{"a": 1}');

      await user.click(screen.getByRole('button', { name: 'Save' }));

      await waitFor(() =>
        expect(configurationsApi.set).toHaveBeenCalledWith('general.extra', { value: { a: 1 } }),
      );
      expect(field).toHaveAccessibleName('Value');
    });

    it('refuses incomplete JSON with a field error and sends nothing', async () => {
      const user = userEvent.setup();
      vi.mocked(configurationsApi.getAll).mockResolvedValue([jsonConfig]);
      render(<ConfigurationSettings />);

      const field = await openEditor();
      await user.clear(field);
      await user.type(field, '{{"a": 1');
      await user.click(screen.getByRole('button', { name: 'Save' }));

      expect(await screen.findByText('Enter valid JSON')).toBeInTheDocument();
      expect(field).toHaveValue('{"a": 1');
      expect(screen.getByRole('dialog', { name: /Edit Configuration/ })).toBeInTheDocument();
      expect(configurationsApi.set).not.toHaveBeenCalled();
    });

    it('saves a typed array and refuses a non-array for an array setting', async () => {
      const user = userEvent.setup();
      vi.mocked(configurationsApi.getAll).mockResolvedValue([arrayConfig]);
      vi.mocked(configurationsApi.set).mockResolvedValue(arrayConfig);
      render(<ConfigurationSettings />);

      let field = await openEditor();
      expect(field).toHaveValue('[]');
      await user.clear(field);
      await user.type(field, '{{}');
      await user.click(screen.getByRole('button', { name: 'Save' }));

      expect(await screen.findByText('Enter a JSON array')).toBeInTheDocument();
      expect(configurationsApi.set).not.toHaveBeenCalled();

      field = within(screen.getByRole('dialog', { name: /Edit Configuration/ })).getByRole('textbox');
      await user.clear(field);
      await user.type(field, '[["x", "y"]');
      await user.click(screen.getByRole('button', { name: 'Save' }));

      await waitFor(() =>
        expect(configurationsApi.set).toHaveBeenCalledWith('general.tags', { value: ['x', 'y'] }),
      );
    });

    it('refuses an empty number', async () => {
      const user = userEvent.setup();
      vi.mocked(configurationsApi.getAll).mockResolvedValue([intConfig]);
      render(<ConfigurationSettings />);

      const field = await openEditor('spinbutton');
      expect(field).toHaveValue(20);
      await user.clear(field);
      await user.click(screen.getByRole('button', { name: 'Save' }));

      expect(await screen.findByText('Enter a number')).toBeInTheDocument();
      expect(configurationsApi.set).not.toHaveBeenCalled();
      expect(field).toHaveAccessibleName('Value');
    });

    it('opens the editor of a row whose list of allowed values is malformed', async () => {
      vi.mocked(configurationsApi.getAll).mockResolvedValue([
        baseConfig({ id: 13, key: 'general.mode', valid_values: '[oops' }),
      ]);
      render(<ConfigurationSettings />);

      const field = await openEditor();
      expect(field).toHaveValue('GopherCRM');
      expect(screen.getByRole('dialog', { name: /Edit Configuration/ })).toBeInTheDocument();
    });
  });

  describe('reset', () => {
    it('asks before resetting and resets only on Confirm', async () => {
      vi.mocked(configurationsApi.reset).mockResolvedValue(baseConfig({}));
      render(<ConfigurationSettings />);

      fireEvent.click(await screen.findByTitle('Reset to default'));
      expect(configurationsApi.reset).not.toHaveBeenCalled();
      const dialog = await screen.findByRole('dialog', { name: 'Reset to default?' });
      expect(dialog).toHaveTextContent('Reset general.company_name to its default value "GoCRM"?');

      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Reset to default?' })).not.toBeInTheDocument(),
      );
      expect(configurationsApi.reset).not.toHaveBeenCalled();

      fireEvent.click(screen.getByTitle('Reset to default'));
      const again = await screen.findByRole('dialog', { name: 'Reset to default?' });
      fireEvent.click(within(again).getByRole('button', { name: 'Reset' }));

      await waitFor(() => expect(refreshConfigurations).toHaveBeenCalledTimes(1));
      expect(configurationsApi.reset).toHaveBeenCalledTimes(1);
      expect(configurationsApi.reset).toHaveBeenCalledWith('general.company_name');
    });
  });

  describe('load error', () => {
    it('shows an error with Retry instead of empty tabs, and loads again on Retry', async () => {
      vi.mocked(configurationsApi.getAll).mockRejectedValueOnce(new Error('offline'));
      render(<ConfigurationSettings />);

      expect(await screen.findByRole('heading', { name: 'Configuration Settings' })).toBeInTheDocument();
      expect(screen.queryByText(/No configurations found/)).not.toBeInTheDocument();
      expect(screen.getByText('Failed to load configurations')).toBeInTheDocument();
      expect(screen.queryByRole('tab')).not.toBeInTheDocument();

      fireEvent.click(screen.getByRole('button', { name: 'Retry' }));

      expect(await screen.findByText('general.company_name')).toBeInTheDocument();
      expect(configurationsApi.getAll).toHaveBeenCalledTimes(2);
    });

    it('shows the server message of a failed load', async () => {
      vi.mocked(configurationsApi.getAll).mockRejectedValueOnce(apiError(503, 'Database unavailable'));
      render(<ConfigurationSettings />);

      expect(await screen.findByText('Database unavailable')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument();
    });
  });
});
