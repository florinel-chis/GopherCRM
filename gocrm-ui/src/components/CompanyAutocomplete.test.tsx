import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { CompanyAutocomplete } from './CompanyAutocomplete';
import { companiesApi } from '@/api/endpoints';
import { createMockCompany } from '@/test/factories';

vi.mock('@/api/endpoints', () => ({
  companiesApi: {
    getCompanies: vi.fn(),
    getCompany: vi.fn(),
  },
}));

const acme = createMockCompany({ id: 7, name: 'Acme Widgets', domain: 'acme.example' });
const bolt = createMockCompany({ id: 8, name: 'Bolt Robotics', domain: '' });

describe('CompanyAutocomplete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies: [acme, bolt], total: 2 });
    vi.mocked(companiesApi.getCompany).mockResolvedValue(acme);
  });

  it('renders a labelled input and does not query until opened', () => {
    render(<CompanyAutocomplete value={null} onChange={vi.fn()} />);

    expect(screen.getByLabelText('Company (linked)')).toBeInTheDocument();
    expect(companiesApi.getCompanies).not.toHaveBeenCalled();
  });

  it('searches as the user types and reports the picked company id', async () => {
    const onChange = vi.fn();
    render(<CompanyAutocomplete value={null} onChange={onChange} />);

    // Focus first: MUI resets the text of an unfocused Autocomplete input to
    // the selected value, so an unfocused change would be thrown away.
    const input = screen.getByLabelText('Company (linked)');
    input.focus();
    fireEvent.change(input, { target: { value: 'acme' } });

    // The typed search must reach the API before the pick closes the list:
    // opening the list already fetched the empty search, whose page also
    // lists Acme, so clicking straight away would prove nothing about typing.
    await waitFor(
      () => expect(companiesApi.getCompanies).toHaveBeenCalledWith({ search: 'acme', limit: 20 }),
      { timeout: 3000 }
    );

    // Name plus domain in brackets; a company without a domain shows its name only.
    fireEvent.click(
      await screen.findByRole('option', { name: 'Acme Widgets (acme.example)' }, { timeout: 3000 })
    );

    expect(onChange).toHaveBeenCalledWith(7, acme);
    await waitFor(
      () => expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)'),
      { timeout: 3000 }
    );
  });

  it('lists a company without a domain by name alone', async () => {
    render(<CompanyAutocomplete value={null} onChange={vi.fn()} />);

    fireEvent.mouseDown(screen.getByLabelText('Company (linked)'));

    expect(
      await screen.findByRole('option', { name: 'Bolt Robotics' }, { timeout: 3000 })
    ).toBeInTheDocument();
  });

  it('preselects from the record the caller already holds without a lookup', () => {
    render(<CompanyAutocomplete value={7} initialCompany={acme} onChange={vi.fn()} />);

    expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
    expect(companiesApi.getCompany).not.toHaveBeenCalled();
  });

  it('looks the company up when only its id is known (a ?company_id= prefill)', async () => {
    render(<CompanyAutocomplete value={7} onChange={vi.fn()} />);

    await waitFor(() => {
      expect(companiesApi.getCompany).toHaveBeenCalledWith(7);
    });
    await waitFor(() => {
      expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
    });
  });

  it('reports null when the selection is cleared', async () => {
    const onChange = vi.fn();
    render(<CompanyAutocomplete value={7} initialCompany={acme} onChange={onChange} />);

    fireEvent.click(screen.getByLabelText('Clear'));

    await waitFor(() => {
      expect(onChange).toHaveBeenCalledWith(null, null);
    });
  });

  it('shows a helper text and error state from the caller', () => {
    render(
      <CompanyAutocomplete value={null} onChange={vi.fn()} error helperText="Unknown company" />
    );

    expect(screen.getByText('Unknown company')).toBeInTheDocument();
  });
});
