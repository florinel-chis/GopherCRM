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
  fireEvent.change(screen.getByLabelText(/Primary Contact Name/i), { target: { value: 'Jane Smith' } });
  fireEvent.change(screen.getByLabelText(/^Email/i), { target: { value: 'jane@acme.example' } });
  fireEvent.change(screen.getByLabelText(/^Phone/i), { target: { value: '+40 21 555 0100' } });
};

const pickCompany = () =>
  pickAutocompleteOption('Company (linked)', 'Acme Widgets (acme.example)');

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

  describe('create mode', () => {
    it('renders the free-text company and the linked company side by side', () => {
      render(<CustomerForm />);

      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toBeInTheDocument();
      expect(screen.getByLabelText('Company (linked)')).toBeInTheDocument();
    });

    it('omits company_id when no company is linked (existing behaviour intact)', async () => {
      render(<CustomerForm />);

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), { target: { value: 'Loose Ltd' } });
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
        { timeout: 3000 }
      );
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

      await waitFor(() => expect(customersApi.createCustomer).toHaveBeenCalledTimes(1), {
        timeout: 3000,
      });
      expect(customersApi.createCustomer).toHaveBeenCalledWith(
        expect.objectContaining({ company_name: 'Acme Widgets', company_id: 7 })
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
        expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
      });

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), { target: { value: 'Acme Widgets' } });
      fillRequired();
      fireEvent.click(screen.getByRole('button', { name: 'Create Customer' }));

      await waitFor(() => {
        expect(customersApi.createCustomer).toHaveBeenCalledWith(expect.objectContaining({ company_id: 7 }));
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
    const existing = createMockCustomer({ id: 5, company_name: 'Acme (typed)', phone: '+40 21 555 0100' });

    beforeEach(() => {
      vi.mocked(useParams).mockReturnValue({ id: '5' });
    });

    it('omits company_id when the customer was never linked and stays so', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue(existing);

      render(<CustomerForm />);
      await waitFor(() => expect(screen.getByDisplayValue('Acme (typed)')).toBeInTheDocument());

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => expect(customersApi.updateCustomer).toHaveBeenCalled());
      expect(vi.mocked(customersApi.updateCustomer).mock.calls[0][1]).not.toHaveProperty('company_id');
    });

    it('preselects the linked company from company_record and sends the id back', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue({ ...existing, company_id: 7, company_record: acme });

      render(<CustomerForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
      });
      expect(companiesApi.getCompany).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => {
        expect(customersApi.updateCustomer).toHaveBeenCalledWith(5, expect.objectContaining({ company_id: 7 }));
      });
    });

    it('sends company_id 0 when the previously linked company is cleared', async () => {
      vi.mocked(customersApi.getCustomer).mockResolvedValue({ ...existing, company_id: 7, company_record: acme });

      render(<CustomerForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
      });

      fireEvent.click(screen.getByLabelText('Clear'));
      await waitFor(() => expect(screen.getByLabelText('Company (linked)')).toHaveValue(''));

      fireEvent.click(screen.getByRole('button', { name: 'Update Customer' }));

      await waitFor(() => {
        expect(customersApi.updateCustomer).toHaveBeenCalledWith(5, expect.objectContaining({ company_id: 0 }));
      });
    });
  });
});
