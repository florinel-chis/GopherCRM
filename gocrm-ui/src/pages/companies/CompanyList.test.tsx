import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as CompanyList } from './CompanyList';
import { companiesApi } from '@/api/endpoints';
import { createMockCompany, createMockUser } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  companiesApi: {
    getCompanies: vi.fn(),
    deleteCompany: vi.fn(),
  },
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

describe('CompanyList', () => {
  const mockNavigate = vi.fn();
  const companies = [
    createMockCompany({ id: 1, name: 'Acme Widgets', domain: 'acme.example', industry: 'Manufacturing' }),
    createMockCompany({ id: 2, name: 'Bolt Robotics', domain: 'bolt.example', industry: 'Robotics', owner: undefined }),
    createMockCompany({ id: 3, name: 'Cobalt Mining', domain: 'cobalt.example', industry: 'Mining' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies, total: 3 });
  });

  it('renders the companies with name, domain, industry and owner', async () => {
    render(<CompanyList />);

    expect(await screen.findByRole('heading', { name: 'Companies' })).toBeInTheDocument();
    expect(screen.getByText('Acme Widgets')).toBeInTheDocument();
    expect(screen.getByText('bolt.example')).toBeInTheDocument();
    expect(screen.getByText('Mining')).toBeInTheDocument();

    const acmeRow = screen.getByText('Acme Widgets').closest('tr') as HTMLElement;
    expect(within(acmeRow).getByText('Test User')).toBeInTheDocument();
    const boltRow = screen.getByText('Bolt Robotics').closest('tr') as HTMLElement;
    expect(within(boltRow).getByText('—')).toBeInTheDocument();
  });

  it('requests the first page with offset/limit and no sort', async () => {
    render(<CompanyList />);

    await waitFor(() => {
      expect(companiesApi.getCompanies).toHaveBeenCalledWith({
        offset: 0,
        limit: 10,
        search: undefined,
        sort_by: undefined,
        sort_order: undefined,
      });
    });
  });

  it('shows the loading state while the list is fetched', () => {
    vi.mocked(companiesApi.getCompanies).mockImplementation(() => new Promise(() => {}));

    render(<CompanyList />);

    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('passes the search term to the API and resets to the first page', async () => {
    render(<CompanyList />);
    await screen.findByText('Acme Widgets');

    fireEvent.change(screen.getByPlaceholderText('Search companies...'), {
      target: { value: 'acme' },
    });

    await waitFor(() => {
      expect(companiesApi.getCompanies).toHaveBeenLastCalledWith(
        expect.objectContaining({ search: 'acme', offset: 0 })
      );
    });
  });

  it('sorts by an allowlisted column through sort_by/sort_order', async () => {
    render(<CompanyList />);
    await screen.findByText('Acme Widgets');

    fireEvent.click(screen.getByRole('button', { name: /^Domain/ }));

    await waitFor(() => {
      expect(companiesApi.getCompanies).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'domain', sort_order: 'asc' })
      );
    });

    // The sort changes the query key, which unmounts the table behind the
    // loader until the new page arrives; wait for the header to come back.
    fireEvent.click(await screen.findByRole('button', { name: /^Domain/ }));

    await waitFor(() => {
      expect(companiesApi.getCompanies).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'domain', sort_order: 'desc' })
      );
    });
  });

  it('does not offer a sort header for columns outside the API allowlist', async () => {
    render(<CompanyList />);
    await screen.findByText('Acme Widgets');

    expect(screen.queryByRole('button', { name: /^City/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Owner/ })).not.toBeInTheDocument();
  });

  it('navigates to the create form from the New Company button', async () => {
    render(<CompanyList />);
    await screen.findByText('Acme Widgets');

    fireEvent.click(screen.getByRole('button', { name: /new company/i }));

    expect(mockNavigate).toHaveBeenCalledWith('/companies/new');
  });

  it('navigates to the detail page on row click', async () => {
    render(<CompanyList />);

    const row = (await screen.findByText('Bolt Robotics')).closest('tr') as HTMLElement;
    fireEvent.click(row);

    expect(mockNavigate).toHaveBeenCalledWith('/companies/2');
  });

  it('deletes a company after confirmation (admin)', async () => {
    vi.mocked(companiesApi.deleteCompany).mockResolvedValue(undefined);

    render(<CompanyList />);

    const row = (await screen.findByText('Acme Widgets')).closest('tr') as HTMLElement;
    const deleteButton = row.querySelector('button svg[data-testid="DeleteIcon"]')?.parentElement;
    fireEvent.click(deleteButton as HTMLElement);

    const dialog = await screen.findByRole('dialog', { name: 'Delete Company' });
    expect(dialog).toHaveTextContent('Acme Widgets');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(companiesApi.deleteCompany).toHaveBeenCalledWith(1);
    });
  });

  it('keeps the company when the delete dialog is cancelled', async () => {
    render(<CompanyList />);

    const row = (await screen.findByText('Acme Widgets')).closest('tr') as HTMLElement;
    fireEvent.click(row.querySelector('button svg[data-testid="DeleteIcon"]')?.parentElement as HTMLElement);

    const dialog = await screen.findByRole('dialog', { name: 'Delete Company' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    expect(companiesApi.deleteCompany).not.toHaveBeenCalled();
  });

  it('hides the delete action and keeps edit for a sales user', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));

    render(<CompanyList />);

    const row = (await screen.findByText('Acme Widgets')).closest('tr') as HTMLElement;
    expect(row.querySelector('svg[data-testid="DeleteIcon"]')).toBeNull();
    expect(row.querySelector('svg[data-testid="EditIcon"]')).not.toBeNull();
    expect(screen.getByRole('button', { name: /new company/i })).toBeInTheDocument();
  });

  it('is read-only for a support user', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));

    render(<CompanyList />);

    const row = (await screen.findByText('Acme Widgets')).closest('tr') as HTMLElement;
    expect(row.querySelector('svg[data-testid="DeleteIcon"]')).toBeNull();
    expect(row.querySelector('svg[data-testid="EditIcon"]')).toBeNull();
    expect(screen.queryByRole('button', { name: /new company/i })).not.toBeInTheDocument();
  });
});
