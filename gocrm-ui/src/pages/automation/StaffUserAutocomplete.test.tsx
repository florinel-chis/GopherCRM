import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { usersApi } from '@/api/endpoints';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import type { User } from '@/types';
import { StaffUserAutocomplete } from './StaffUserAutocomplete';

vi.mock('@/api/endpoints', () => ({
  usersApi: {
    getUsers: vi.fn(),
    getUser: vi.fn(),
  },
}));

const page = (data: User[], total: number) => ({ data, total, page: 1, limit: 100, total_pages: 1 });

const sam = createMockUser({ id: 7, first_name: 'Sam', last_name: 'Seller', role: 'sales' });
const ada = createMockUser({ id: 8, first_name: 'Ada', last_name: 'Admin', role: 'admin' });
const gone = createMockUser({ id: 9, first_name: 'Gone', last_name: 'Away', role: 'support', is_active: false });
const cora = createMockUser({ id: 10, first_name: 'Cora', last_name: 'Client', role: 'customer' });

describe('StaffUserAutocomplete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('offers only active staff users: inactive and customer accounts are left out', async () => {
    vi.mocked(usersApi.getUsers).mockResolvedValue(page([sam, gone, cora, ada], 4));
    const onChange = vi.fn();
    render(<StaffUserAutocomplete label="User" value={null} onChange={onChange} />);

    await pickAutocompleteOption('User', 'Sam Seller (sales)');

    expect(onChange).toHaveBeenCalledWith(7);
    expect(usersApi.getUsers).toHaveBeenCalledTimes(1);
    expect(usersApi.getUsers).toHaveBeenCalledWith({ search: undefined, offset: 0, limit: 100 });

    fireEvent.mouseDown(screen.getByLabelText('User'));
    const options = (await screen.findAllByRole('option')).map((option) => option.textContent);
    expect(options).toEqual(['Sam Seller (sales)', 'Ada Admin (admin)']);
  });

  it('fetches later pages until it finds eligible users', async () => {
    const customers = Array.from({ length: 100 }, (_, i) =>
      createMockUser({ id: 100 + i, first_name: 'C', last_name: String(i), role: 'customer' }),
    );
    vi.mocked(usersApi.getUsers).mockImplementation(async (filters) =>
      filters?.offset === 0 ? page(customers, 150) : page([sam], 150),
    );
    render(<StaffUserAutocomplete label="User" value={null} onChange={vi.fn()} />);

    await pickAutocompleteOption('User', 'Sam Seller (sales)');

    expect(vi.mocked(usersApi.getUsers).mock.calls.map(([filters]) => filters)).toEqual([
      { search: undefined, offset: 0, limit: 100 },
      { search: undefined, offset: 100, limit: 100 },
    ]);
  });

  it('shows a selected user that is not on the fetched pages through a lookup', async () => {
    vi.mocked(usersApi.getUser).mockResolvedValue(ada);
    render(<StaffUserAutocomplete label="New owner" value={8} onChange={vi.fn()} />);

    await waitFor(() => expect(screen.getByLabelText('New owner')).toHaveValue('Ada Admin (admin)'));
    expect(usersApi.getUser).toHaveBeenCalledWith(8);
    expect(usersApi.getUsers).not.toHaveBeenCalled();
    expect(screen.queryByText('This user can no longer be assigned')).not.toBeInTheDocument();
  });

  it('flags a selected user who is inactive', async () => {
    vi.mocked(usersApi.getUser).mockImplementation(async (id) => (id === 9 ? gone : cora));
    const { unmount } = render(<StaffUserAutocomplete label="User" value={9} onChange={vi.fn()} />);

    await waitFor(() => expect(screen.getByLabelText('User')).toHaveValue('Gone Away (inactive)'));
    expect(screen.getByText('This user can no longer be assigned')).toBeInTheDocument();
    expect(screen.getByLabelText('User')).toHaveAttribute('aria-invalid', 'true');
    unmount();

    render(<StaffUserAutocomplete label="User" value={10} onChange={vi.fn()} />);
    await waitFor(() => expect(screen.getByLabelText('User')).toHaveValue('Cora Client (customer)'));
    expect(screen.getByText('This user can no longer be assigned')).toBeInTheDocument();
  });

  it('shows a field error message instead of the inactive hint', async () => {
    vi.mocked(usersApi.getUser).mockResolvedValue(gone);
    render(
      <StaffUserAutocomplete label="User" value={9} onChange={vi.fn()} error helperText="the user is deactivated" />,
    );

    await waitFor(() => expect(screen.getByLabelText('User')).toHaveValue('Gone Away (inactive)'));
    expect(screen.getByText('the user is deactivated')).toBeInTheDocument();
    expect(screen.queryByText('This user can no longer be assigned')).not.toBeInTheDocument();
  });

  it('reports a failed user search or lookup instead of an empty list', async () => {
    const failure = new AxiosError('Request failed with status code 500', 'ERR_BAD_RESPONSE', undefined, undefined, {
      status: 500,
      statusText: 'Error',
      headers: {},
      config: {} as InternalAxiosRequestConfig,
      data: { code: 'INTERNAL_ERROR', message: 'Database unavailable', details: null },
    } as AxiosResponse);
    vi.mocked(usersApi.getUsers).mockRejectedValue(failure);
    const { unmount } = render(<StaffUserAutocomplete label="User" value={null} onChange={vi.fn()} />);

    const input = screen.getByLabelText('User');
    input.focus();
    fireEvent.mouseDown(input);
    expect(await screen.findByText('Users could not be loaded: Database unavailable')).toBeInTheDocument();
    expect(input).toHaveAttribute('aria-invalid', 'true');
    unmount();

    vi.mocked(usersApi.getUser).mockRejectedValue(failure);
    render(<StaffUserAutocomplete label="User" value={8} onChange={vi.fn()} />);
    expect(await screen.findByText('User #8 could not be loaded: Database unavailable')).toBeInTheDocument();
  });

  it('a late failure of an older search does not flag the newer successful one', async () => {
    let failOld: (reason: unknown) => void = () => undefined;
    vi.mocked(usersApi.getUsers).mockImplementation((filters) =>
      filters?.search === 'sam'
        ? Promise.resolve(page([sam], 1))
        : new Promise((_resolve, reject) => {
            failOld = reject;
          }),
    );
    render(<StaffUserAutocomplete label="User" value={null} onChange={vi.fn()} />);

    const input = screen.getByLabelText('User');
    input.focus();
    fireEvent.mouseDown(input);
    await waitFor(() => expect(usersApi.getUsers).toHaveBeenCalledTimes(1));
    fireEvent.change(input, { target: { value: 'sam' } });
    expect(await screen.findByRole('option', { name: 'Sam Seller (sales)' }, { timeout: 3000 })).toBeInTheDocument();

    failOld(new Error('Network Error'));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByText(/could not be loaded/)).not.toBeInTheDocument();
    expect(input).not.toHaveAttribute('aria-invalid', 'true');
  });
});
