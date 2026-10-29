import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as CustomerForm } from './CustomerForm';
import { customersApi, companiesApi } from '@/api/endpoints';
import { createMockCustomer, createMockCompany } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
    useSearchParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  customersApi: {
    getCustomer: vi.fn(),
    createCustomer: vi.fn(),
    updateCustomer: vi.fn(),
  },
  companiesApi: {
    getCompanies: vi.fn(),
    getCompany: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}));

const acme = createMockCompany({ id: 7, name: 'Acme Widgets', domain: 'acme.example' });

// The rejection the form sees for an unknown company_id: the API answers
// `{success:false, error:{code:"INVALID_REFERENCE", message, details:null}}`
// and the axios client (src/api/client.ts) replaces `response.data` with that
// `error` object before the endpoint module rethrows.
const invalidReference = (message: string) => ({
  response: { status: 400, data: { code: 'INVALID_REFERENCE', message, details: null } },
});

const fillRequired = () => {
  fireEvent.change(screen.getByLabelText(/Primary Contact Name/i), {
    target: { value: 'Jane Smith' },
  });
  fireEvent.change(screen.getByLabelText(/^Email/i), { target: { value: 'jane@acme.example' } });
  fireEvent.change(screen.getByLabelText(/^Phone/i), { target: { value: '+40 21 555 0100' } });
};

const pickCompany = () => pickAutocompleteOption('Company (linked)', 'Acme Widgets (acme.example)');

describe('CustomerForm linked company', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    vi.mocked(useSearchParams).mockReturnValue([new URLSearchParams(), vi.fn()]);
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies: [acme], total: 1 });
    vi.mocked(companiesApi.getCompany).mockResolvedValue(acme);
    vi.mocked(customersApi.createCustomer).mockResolvedValue(createMockCustomer());
    vi.mocked(customersApi.updateCustomer).mockResolvedValue(createMockCustomer());
  });

  it('submits a trimmed website on creation', async () => {
    render(<CustomerForm />);
    fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
      target: { value: 'Example' },
    });
    fillRequired();
    fireEvent.change(screen.getByLabelText('Website'), {
      target: { value: '  https://example.com  ' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));
    await waitFor(() =>
      expect(customersApi.createCustomer).toHaveBeenCalledWith(
        expect.objectContaining({ website: 'https://example.com' }),
      ),
    );
  });

  it('loads a saved website and sends an empty string when cleared', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(customersApi.getCustomer).mockResolvedValue(
      createMockCustomer({ id: 5, website: 'https://example.com', phone: '123' }),
    );
    render(<CustomerForm />);
    await waitFor(() =>
      expect(screen.getByLabelText('Website')).toHaveValue('https://example.com'),
    );
    fireEvent.change(screen.getByLabelText('Website'), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));
    await waitFor(() =>
      expect(customersApi.updateCustomer).toHaveBeenCalledWith(
        5,
        expect.objectContaining({ website: '' }),
      ),
    );
  });

  describe('create mode', () => {
    it('renders the free-text company and the linked company side by side', () => {
      render(<CustomerForm />);

      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toBeInTheDocument();
      expect(screen.getByLabelText('Company (linked)')).toBeInTheDocument();
    });

    it('omits company_id when no company is linked (existing behaviour intact)', async () => {
      render(<CustomerForm />);

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
        target: { value: 'Loose Ltd' },
      });
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

      await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalled());
      const sent = vi.mocked(customersApi.createCustomer).mock.calls[0][0];
      expect(sent.company_name).toBe('Loose Ltd');
      expect(sent).not.toHaveProperty('company_id');
      expect(mockNavigate).toHaveBeenCalledWith('/customers');
    });

    it('sends company_id for the picked company and seeds the blank free-text company', async () => {
      render(<CustomerForm />);

      await pickCompany();
      await waitFor(
        () => expect(screen.getByLabelText(/^Company \(as entered\)/i)).toHaveValue('Acme Widgets'),
        { timeout: 3000 },
      );
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

      await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalledTimes(1), {
        timeout: 3000,
      });
      expect(customersApi.createCustomer).toHaveBeenCalledWith(
        expect.objectContaining({ company_name: 'Acme Widgets', company_id: 7 }),
      );
    });

    it('puts an INVALID_REFERENCE message on the linked-company picker', async () => {
      const message = 'unknown company_id 7: company not found';
      vi.mocked(customersApi.createCustomer).mockRejectedValue(invalidReference(message));

      render(<CustomerForm />);
      await pickCompany();
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: /Create Customer/i }));

      await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalledTimes(1), {
        timeout: 3000,
      });
      expect(await screen.findByText(message, undefined, { timeout: 3000 })).toBeInTheDocument();
      expect(screen.getByLabelText('Company (linked)')).toHaveAccessibleDescription(message);
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('prefills the linked company from ?company_id= (the New Customer button on a company)', async () => {
      vi.mocked(useSearchParams).mockReturnValue([new URLSearchParams('company_id=7'), vi.fn()]);

      render(<CustomerForm />);

      await waitFor(() => expect(companiesApi.getCompany).toHaveBeenCalledWith(7));
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue(
          'Acme Widgets (acme.example)',
        );
      });

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
        target: { value: 'Acme Widgets' },
      });
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

      await waitFor(() => {
        expect(customersApi.createCustomer).toHaveBeenCalledWith(
          expect.objectContaining({ company_id: 7 }),
        );
      });
    });

    it('ignores a malformed ?company_id=', () => {
      vi.mocked(useSearchParams).mockReturnValue([new URLSearchParams('company_id=abc'), vi.fn()]);

      render(<CustomerForm />);

      expect(companiesApi.getCompany).not.toHaveBeenCalled();
      expect(screen.getByLabelText('Company (linked)')).toHaveValue('');
    });
  });

  describe('edit mode', () => {
    const existing = createMockCustomer({
      id: 5,
      company_name: 'Acme (typed)',
      phone: '+40 21 555 0100',
    });

    beforeEach(() => {
      vi.mocked(useParams).mockReturnValue({ id: '5' });
    });

    it('omits company_id when the customer was never linked and stays so', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue(existing);

      render(<CustomerForm />);
      await waitFor(() => expect(screen.getByDisplayValue('Acme (typed)')).toBeInTheDocument());

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => expect(customersApi.updateCustomer).toHaveBeenCalled());
      expect(vi.mocked(customersApi.updateCustomer).mock.calls[0][1]).not.toHaveProperty(
        'company_id',
      );
    });

    it('preselects the linked company from company_record and sends the id back', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue({
        ...existing,
        company_id: 7,
        company_record: acme,
      });

      render(<CustomerForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue(
          'Acme Widgets (acme.example)',
        );
      });
      expect(companiesApi.getCompany).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => {
        expect(customersApi.updateCustomer).toHaveBeenCalledWith(
          5,
          expect.objectContaining({ company_id: 7 }),
        );
      });
    });

    it('sends company_id 0 when the previously linked company is cleared', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue({
        ...existing,
        company_id: 7,
        company_record: acme,
      });

      render(<CustomerForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue(
          'Acme Widgets (acme.example)',
        );
      });

      fireEvent.click(screen.getByLabelText('Clear'));
      await waitFor(() => expect(screen.getByLabelText('Company (linked)')).toHaveValue(''));

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => {
        expect(customersApi.updateCustomer).toHaveBeenCalledWith(
          5,
          expect.objectContaining({ company_id: 0 }),
        );
      });
    });
  });
});

// The form offers only what the customer API persists. Industry and employee
// range belong to the linked Company; there is no customer activation.
describe('CustomerForm retained fields', () => {
  const mockNavigate = vi.fn();

  const retained = {
    company_name: 'Bolt Robotics',
    contact_name: 'Ada Byron',
    email: 'ada@bolt.example',
    phone: '+40 21 555 0199',
    website: 'https://bolt.example',
    address: '1 Gear Street',
    city: 'Cluj',
    state: 'CJ',
    country: 'Romania',
    postal_code: '400000',
    notes: 'Prefers email',
  };

  const labels: Array<[keyof typeof retained, RegExp]> = [
    ['company_name', /^Company \(as entered\)/i],
    ['contact_name', /Primary Contact Name/i],
    ['email', /^Email/i],
    ['phone', /^Phone/i],
    ['website', /^Website/i],
    ['address', /Street Address/i],
    ['city', /^City/i],
    ['state', /State\/Province/i],
    ['country', /^Country/i],
    ['postal_code', /Postal Code/i],
    ['notes', /^Notes/i],
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    vi.mocked(useSearchParams).mockReturnValue([new URLSearchParams(), vi.fn()]);
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies: [], total: 0 });
    vi.mocked(customersApi.createCustomer).mockResolvedValue(createMockCustomer());
    vi.mocked(customersApi.updateCustomer).mockResolvedValue(createMockCustomer());
  });

  it('renders no industry, employee count, annual revenue or active-status control', () => {
    render(<CustomerForm />);

    expect(screen.queryByLabelText(/Industry/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Number of Employees/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Annual Revenue/i)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Active Customer/i)).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
  });

  it('creates with exactly the retained fields', async () => {
    render(<CustomerForm />);

    for (const [field, label] of labels) {
      fireEvent.change(screen.getByLabelText(label), { target: { value: retained[field] } });
    }
    fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

    await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalledTimes(1));
    expect(vi.mocked(customersApi.createCustomer).mock.calls[0][0]).toStrictEqual(retained);
  });

  it('creates with the optional fields left empty', async () => {
    render(<CustomerForm />);

    fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
      target: { value: 'Bare Ltd' },
    });
    fillRequired();
    fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

    await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalledTimes(1));
    expect(vi.mocked(customersApi.createCustomer).mock.calls[0][0]).toStrictEqual({
      company_name: 'Bare Ltd',
      contact_name: 'Jane Smith',
      email: 'jane@acme.example',
      phone: '+40 21 555 0100',
      website: '',
      address: '',
      city: '',
      state: '',
      country: '',
      postal_code: '',
      notes: '',
    });
  });

  it('reloads every retained field on edit and sends empty strings for the cleared ones', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '9' });
    vi.mocked(customersApi.getCustomer).mockResolvedValue(
      createMockCustomer({ id: 9, ...retained }),
    );

    render(<CustomerForm />);

    await waitFor(() => expect(screen.getByLabelText(/^Notes/i)).toHaveValue(retained.notes));
    for (const [field, label] of labels) {
      expect(screen.getByLabelText(label)).toHaveValue(retained[field]);
    }

    const optional = ['website', 'address', 'city', 'state', 'country', 'postal_code', 'notes'] as const;
    for (const [field, label] of labels) {
      if ((optional as readonly string[]).includes(field)) {
        fireEvent.change(screen.getByLabelText(label), { target: { value: '' } });
      }
    }
    fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

    await waitFor(() => expect(customersApi.updateCustomer).toHaveBeenCalledTimes(1));
    const [id, sent] = vi.mocked(customersApi.updateCustomer).mock.calls[0];
    expect(id).toBe(9);
    expect(sent).toStrictEqual({
      company_name: retained.company_name,
      contact_name: retained.contact_name,
      email: retained.email,
      phone: retained.phone,
      website: '',
      address: '',
      city: '',
      state: '',
      country: '',
      postal_code: '',
      notes: '',
    });
  });
});
