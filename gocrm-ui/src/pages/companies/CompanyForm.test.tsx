import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as CompanyForm } from './CompanyForm';
import { companiesApi, usersApi } from '@/api/endpoints';
import { createMockCompany, createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { useNavigate, useParams } from 'react-router-dom';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  companiesApi: {
    getCompany: vi.fn(),
    createCompany: vi.fn(),
    updateCompany: vi.fn(),
  },
  usersApi: {
    getUsers: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

// The rejection the form sees: the API answers
// `{success:false, error:{code, message, details}, meta}` and the axios client
// (src/api/client.ts) replaces `response.data` with that `error` object before
// the endpoint module rethrows.
const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: status === 409 ? 'Conflict' : 'Bad Request',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);
const axios409 = (message: string) => apiError(409, 'CONFLICT', message);

// Required fields carry MUI's " *" suffix in their label, hence the prefix match.
const fill = (label: string, value: string) => {
  fireEvent.change(screen.getByLabelText(new RegExp(`^${label.replace('/', '\\/')}$|^${label} \\*`)), {
    target: { value },
  });
};

describe('CompanyForm', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(usersApi.getUsers).mockResolvedValue({
      data: [
        createMockUser({ id: 1, first_name: 'Ana', last_name: 'Pop' }),
        createMockUser({ id: 2, first_name: 'Ion', last_name: 'Ionescu' }),
      ],
      total: 2,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  describe('create mode', () => {
    it('renders every field including the owner picker for an admin', async () => {
      render(<CompanyForm />);

      expect(screen.getByRole('heading', { name: 'Create New Company' })).toBeInTheDocument();
      for (const label of [
        'Name',
        'Domain',
        'Website',
        'Industry',
        'Employees',
        'Phone',
        'Owner',
        'Street Address',
        'City',
        'State/Province',
        'Country',
        'Postal Code',
        'Notes',
      ]) {
        expect(screen.getByLabelText(new RegExp(`^${label}`))).toBeInTheDocument();
      }
      expect(screen.getByLabelText(/^Owner/)).toHaveAccessibleDescription(
        'Account manager; leave empty to keep the company unowned'
      );
      await waitFor(() => expect(usersApi.getUsers).toHaveBeenCalledWith({ is_active: true }));
    });

    it('hides the owner picker and never loads users for a sales user', () => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));

      render(<CompanyForm />);

      expect(screen.queryByLabelText(/^Owner/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Account manager/)).not.toBeInTheDocument();
      expect(usersApi.getUsers).not.toHaveBeenCalled();
    });

    it('requires a name and refuses a non-http website', async () => {
      render(<CompanyForm />);

      fill('Website', 'not a url');
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      expect(await screen.findByText('Name is required')).toBeInTheDocument();
      expect(screen.getByText('Website must be an http(s) URL')).toBeInTheDocument();
      expect(companiesApi.createCompany).not.toHaveBeenCalled();
    });

    it('enforces the column bounds', async () => {
      render(<CompanyForm />);

      fill('Name', 'x'.repeat(201));
      fill('Postal Code', '1'.repeat(21));
      fill('Phone', '1'.repeat(51));
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      expect(await screen.findByText('Name must be 200 characters or fewer')).toBeInTheDocument();
      expect(screen.getByText('Postal code must be 20 characters or fewer')).toBeInTheDocument();
      expect(screen.getByText('Phone must be 50 characters or fewer')).toBeInTheDocument();
      expect(companiesApi.createCompany).not.toHaveBeenCalled();
    });

    it('submits the trimmed fields, the employee range and the chosen owner, then opens the new company', async () => {
      vi.mocked(companiesApi.createCompany).mockResolvedValue(createMockCompany({ id: 42 }));

      render(<CompanyForm />);

      fill('Name', '  Acme Widgets  ');
      fill('Domain', 'acme.example');
      fill('Website', 'https://www.acme.example');
      fill('Industry', 'Manufacturing');
      fill('Phone', '+40 21 555 0100');
      fill('Street Address', '1 Foundry Lane');
      fill('City', 'Cluj');
      fill('State/Province', 'CJ');
      fill('Country', 'Romania');
      fill('Postal Code', '400001');
      fill('Notes', 'Key account');

      fireEvent.mouseDown(screen.getByLabelText(/^Employees/));
      fireEvent.click(await screen.findByRole('option', { name: '51-200' }));

      await pickAutocompleteOption(/^Owner/, 'Ion', 'Ion Ionescu');

      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      await waitFor(() => expect(companiesApi.createCompany).toHaveBeenCalledTimes(1), {
        timeout: 3000,
      });
      expect(companiesApi.createCompany).toHaveBeenCalledWith({
        name: 'Acme Widgets',
        domain: 'acme.example',
        website: 'https://www.acme.example',
        industry: 'Manufacturing',
        employee_range: '51-200',
        phone: '+40 21 555 0100',
        address: '1 Foundry Lane',
        city: 'Cluj',
        state: 'CJ',
        country: 'Romania',
        postal_code: '400001',
        notes: 'Key account',
        owner_id: 2,
      });
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/companies/42'), { timeout: 3000 });
      expect(showSuccess).toHaveBeenCalledWith('Company created successfully');
    });

    it('sends no owner_id for a sales user', async () => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
      vi.mocked(companiesApi.createCompany).mockResolvedValue(createMockCompany({ id: 43 }));

      render(<CompanyForm />);

      fill('Name', 'Bolt Robotics');
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      await waitFor(() => {
        expect(companiesApi.createCompany).toHaveBeenCalledWith(
          expect.objectContaining({ name: 'Bolt Robotics' })
        );
      });
      expect(vi.mocked(companiesApi.createCompany).mock.calls[0][0]).not.toHaveProperty('owner_id');
    });

    it('shows the server message on the domain field after a 409', async () => {
      const message = 'A company with the domain acme.example already exists';
      vi.mocked(companiesApi.createCompany).mockRejectedValue(axios409(message));

      render(<CompanyForm />);

      fill('Name', 'Acme Widgets');
      fill('Domain', 'acme.example');
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(screen.getByLabelText(/^Domain/)).toHaveAccessibleDescription(message);
      expect(showError).toHaveBeenCalledWith(message);
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('shows a 400 validation message from the server instead of the generic text', async () => {
      const message = 'website must be an http or https URL: validation failed';
      vi.mocked(companiesApi.createCompany).mockRejectedValue(
        apiError(400, 'VALIDATION_ERROR', message)
      );

      render(<CompanyForm />);

      fill('Name', 'Acme Widgets');
      fill('Website', 'https://acme.example');
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      await waitFor(() => expect(companiesApi.createCompany).toHaveBeenCalledTimes(1));
      await waitFor(() => expect(showError).toHaveBeenCalledWith(message), { timeout: 3000 });
      expect(showError).not.toHaveBeenCalledWith('Failed to create company');
      expect(screen.getByLabelText(/^Domain/)).not.toHaveAccessibleDescription(message);
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('reports other failures generically', async () => {
      vi.mocked(companiesApi.createCompany).mockRejectedValue(new Error('Network Error'));

      render(<CompanyForm />);

      fill('Name', 'Acme Widgets');
      fireEvent.click(screen.getByRole('button', { name: 'Create Company' }));

      await waitFor(() => expect(showError).toHaveBeenCalledWith('Failed to create company'));
    });

    it('returns to the list on cancel', () => {
      render(<CompanyForm />);

      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

      expect(mockNavigate).toHaveBeenCalledWith('/companies');
    });
  });

  describe('edit mode', () => {
    const existing = createMockCompany({
      id: 7,
      name: 'Acme Widgets',
      domain: 'acme.example',
      industry: 'Manufacturing',
      employee_range: '51-200',
      owner: createMockUser({ id: 2, first_name: 'Ion', last_name: 'Ionescu' }),
      owner_id: 2,
    });

    beforeEach(() => {
      vi.mocked(useParams).mockReturnValue({ id: '7' });
      vi.mocked(companiesApi.getCompany).mockResolvedValue(existing);
    });

    it('loads the company into the form', async () => {
      render(<CompanyForm />);

      expect(await screen.findByRole('heading', { name: 'Edit Company' })).toBeInTheDocument();
      await waitFor(() => {
        expect(screen.getByDisplayValue('Acme Widgets')).toBeInTheDocument();
      });
      expect(screen.getByDisplayValue('acme.example')).toBeInTheDocument();
      expect(screen.getByDisplayValue('Manufacturing')).toBeInTheDocument();
      expect(screen.getByLabelText(/^Employees/)).toHaveTextContent('51-200');
      await waitFor(() => {
        expect(screen.getByLabelText(/^Owner/)).toHaveValue('Ion Ionescu');
      });
      expect(screen.getByLabelText(/^Owner/)).toHaveAccessibleDescription(
        'Account manager; clear to remove the owner'
      );
    });

    it('PUTs the full body without owner_id when the owner is untouched and returns to the detail page', async () => {
      vi.mocked(companiesApi.updateCompany).mockResolvedValue(existing);

      render(<CompanyForm />);
      await waitFor(() => expect(screen.getByLabelText(/^Owner/)).toHaveValue('Ion Ionescu'));

      fill('Industry', 'Robotics');
      fireEvent.click(screen.getByRole('button', { name: 'Update Company' }));

      await waitFor(() => {
        expect(companiesApi.updateCompany).toHaveBeenCalledWith(7, {
          name: 'Acme Widgets',
          domain: 'acme.example',
          website: existing.website,
          industry: 'Robotics',
          employee_range: '51-200',
          phone: existing.phone,
          address: existing.address,
          city: existing.city,
          state: existing.state,
          country: existing.country,
          postal_code: existing.postal_code,
          notes: '',
        });
      });
      expect(vi.mocked(companiesApi.updateCompany).mock.calls[0][1]).not.toHaveProperty('owner_id');
      expect(mockNavigate).toHaveBeenCalledWith('/companies/7');
    });

    it('sends owner_id 0 when an admin clears the owner', async () => {
      vi.mocked(companiesApi.updateCompany).mockResolvedValue(existing);

      render(<CompanyForm />);
      await waitFor(() => expect(screen.getByLabelText(/^Owner/)).toHaveValue('Ion Ionescu'));

      fireEvent.click(screen.getByTitle('Clear'));
      await waitFor(() => expect(screen.getByLabelText(/^Owner/)).toHaveValue(''), { timeout: 3000 });
      fireEvent.click(screen.getByRole('button', { name: 'Update Company' }));

      await waitFor(() => {
        expect(companiesApi.updateCompany).toHaveBeenCalledWith(
          7,
          expect.objectContaining({ name: 'Acme Widgets', owner_id: 0 })
        );
      });
    });

    it('sends the new owner_id when an admin picks another owner', async () => {
      vi.mocked(companiesApi.updateCompany).mockResolvedValue(existing);

      render(<CompanyForm />);
      await waitFor(() => expect(screen.getByLabelText(/^Owner/)).toHaveValue('Ion Ionescu'));

      await pickAutocompleteOption(/^Owner/, 'Ana', 'Ana Pop');
      fireEvent.click(screen.getByRole('button', { name: 'Update Company' }));

      await waitFor(() => expect(companiesApi.updateCompany).toHaveBeenCalledTimes(1), {
        timeout: 3000,
      });
      expect(companiesApi.updateCompany).toHaveBeenCalledWith(
        7,
        expect.objectContaining({ owner_id: 1 })
      );
    });

    it('shows the 409 message on the domain field when an update collides', async () => {
      const message = 'Domain already in use';
      vi.mocked(companiesApi.updateCompany).mockRejectedValue(axios409(message));

      render(<CompanyForm />);
      await waitFor(() => expect(screen.getByDisplayValue('Acme Widgets')).toBeInTheDocument());

      fill('Domain', 'bolt.example');
      fireEvent.click(screen.getByRole('button', { name: 'Update Company' }));

      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('cancel returns to the detail page', async () => {
      render(<CompanyForm />);
      await waitFor(() => expect(screen.getByDisplayValue('Acme Widgets')).toBeInTheDocument());

      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

      expect(mockNavigate).toHaveBeenCalledWith('/companies/7');
    });
  });
});
