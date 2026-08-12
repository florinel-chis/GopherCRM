import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as ConfigurationSettings } from './ConfigurationSettings';
import { configurationsApi } from '@/api/endpoints/configurations';
import type { Configuration } from '@/api/endpoints/configurations';

const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/configurations', () => ({
  configurationsApi: {
    getAll: vi.fn(),
    getUIConfigurations: vi.fn(),
    set: vi.fn(),
    reset: vi.fn(),
    getValue: vi.fn((config: Configuration) => config.value),
  },
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

const openIntegrationTab = async () => {
  const tab = await screen.findByRole('tab', { name: /Integration/ });
  fireEvent.click(tab);
};

describe('ConfigurationSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (configurationsApi.getAll as any).mockResolvedValue([baseConfig({}), sensitive]);
    (configurationsApi.set as any).mockResolvedValue(sensitive);
  });

  it('shows a configured chip instead of a value for a sensitive row', async () => {
    render(<ConfigurationSettings />);

    await openIntegrationTab();

    const chip = await screen.findByTestId('sensitive-chip-integration.aeo.gemini_api_key');
    expect(chip).toHaveTextContent('Configured');
  });

  it('marks an unset sensitive row as not configured', async () => {
    (configurationsApi.getAll as any).mockResolvedValue([
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

  it('keeps the plain editor for a non-sensitive row', async () => {
    render(<ConfigurationSettings />);

    fireEvent.click(await screen.findByTitle('Edit configuration'));

    await waitFor(() => {
      expect(screen.getByDisplayValue('GopherCRM')).toBeInTheDocument();
    });
    expect(screen.queryByLabelText('New value')).not.toBeInTheDocument();
  });
});
