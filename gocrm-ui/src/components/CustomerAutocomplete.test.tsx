import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { CustomerAutocomplete } from './CustomerAutocomplete';
import { customersApi } from '@/api/endpoints';
import { createMockCustomer } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';

vi.mock('@/api/endpoints', () => ({
  customersApi: {
    getCustomers: vi.fn(),
    getCustomer: vi.fn(),
  },
}));

const jane = createMockCustomer({ id: 3, contact_name: 'Jane Smith', company_name: 'Acme Widgets' });
const noCompany = createMockCustomer({ id: 4, contact_name: 'Joe Bloggs', company_name: '' });

describe('CustomerAutocomplete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(customersApi.getCustomers).mockResolvedValue({
      data: [jane, noCompany],
      total: 2,
      page: 1,
      limit: 20,
      total_pages: 1,
    });
    vi.mocked(customersApi.getCustomer).mockResolvedValue(jane);
  });

  it('renders a labelled input and does not query until opened', () => {
    render(<CustomerAutocomplete value={null} onChange={vi.fn()} />);

    expect(screen.getByLabelText('Customer')).toBeInTheDocument();
    expect(customersApi.getCustomers).not.toHaveBeenCalled();
  });

  it('opens with one search page and reports the picked customer id', async () => {
    const onChange = vi.fn();
    render(<CustomerAutocomplete value={null} onChange={onChange} />);

    await pickAutocompleteOption('Customer', 'Jane Smith (Acme Widgets)');

    expect(customersApi.getCustomers).toHaveBeenCalledWith({ search: undefined, limit: 20 });
    expect(onChange).toHaveBeenCalledWith(3, jane);
  });

  it('searches as the user types', async () => {
    render(<CustomerAutocomplete value={null} onChange={vi.fn()} />);

    const input = screen.getByLabelText('Customer');
    input.focus();
    fireEvent.change(input, { target: { value: 'jane' } });

    await waitFor(
      () => expect(customersApi.getCustomers).toHaveBeenCalledWith({ search: 'jane', limit: 20 }),
      { timeout: 3000 }
    );
  });

  it('lists a customer without a company by name alone', async () => {
    render(<CustomerAutocomplete value={null} onChange={vi.fn()} />);

    fireEvent.mouseDown(screen.getByLabelText('Customer'));

    expect(await screen.findByRole('option', { name: 'Joe Bloggs' }, { timeout: 3000 })).toBeInTheDocument();
  });

  it('preselects from the record the caller already holds without a lookup', () => {
    render(<CustomerAutocomplete value={3} initialCustomer={jane} onChange={vi.fn()} />);

    expect(screen.getByLabelText('Customer')).toHaveValue('Jane Smith (Acme Widgets)');
    expect(customersApi.getCustomer).not.toHaveBeenCalled();
  });

  it('looks the customer up when only its id is known (a ?customer_id= prefill)', async () => {
    render(<CustomerAutocomplete value={3} onChange={vi.fn()} />);

    await waitFor(() => expect(customersApi.getCustomer).toHaveBeenCalledWith(3));
    await waitFor(() => expect(screen.getByLabelText('Customer')).toHaveValue('Jane Smith (Acme Widgets)'));
  });

  it('reports null when the selection is cleared', async () => {
    const onChange = vi.fn();
    render(<CustomerAutocomplete value={3} initialCustomer={jane} onChange={onChange} />);

    fireEvent.click(screen.getByLabelText('Clear'));

    await waitFor(() => expect(onChange).toHaveBeenCalledWith(null, null));
  });

  it('shows a helper text and error state from the caller', () => {
    render(<CustomerAutocomplete value={null} onChange={vi.fn()} error helperText="Unknown customer" />);

    expect(screen.getByText('Unknown customer')).toBeInTheDocument();
  });
});
