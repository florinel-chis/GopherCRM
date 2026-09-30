import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as DealForm } from './DealForm';
import { companiesApi, configurationsApi, customersApi, dealsApi, leadsApi, usersApi } from '@/api/endpoints';
import type { Configuration } from '@/api/endpoints';
import {
  createMockCompany,
  createMockCustomer,
  createMockDeal,
  createMockLead,
  createMockUser,
} from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import type { User } from '@/types';
import { DASHBOARD_PIPELINE_KEY, clientWithDashboardPipeline, isInvalidated, withClient } from '@/test/dealQueryCache';

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
  dealsApi: {
    getDeal: vi.fn(),
    createDeal: vi.fn(),
    updateDeal: vi.fn(),
  },
  usersApi: {
    getUsers: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
  },
  companiesApi: {
    getCompanies: vi.fn(),
    getCompany: vi.fn(),
  },
  customersApi: {
    getCustomers: vi.fn(),
    getCustomer: vi.fn(),
  },
  leadsApi: {
    getLeads: vi.fn(),
    getLead: vi.fn(),
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
// replaces `response.data` with that `error` object before rethrowing.
const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Bad Request',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const currencyConfig = (value: string): Configuration => ({
  id: 1,
  key: 'deals.default_currency',
  value,
  type: 'string',
  category: 'general',
  description: '',
  default_value: 'EUR',
  is_system: false,
  is_read_only: false,
  valid_values: '',
  created_at: '',
  updated_at: '',
});

const acme = createMockCompany({ id: 7, name: 'Acme Widgets', domain: 'acme.example' });
const jane = createMockCustomer({ id: 3, contact_name: 'Jane Smith', company_name: 'Acme Widgets' });
const ion = createMockLead({ id: 5, contact_name: 'Ion Ionescu', company_name: 'Bolt Robotics' });

const setSearch = (query: string) =>
  vi.mocked(useSearchParams).mockReturnValue([new URLSearchParams(query), vi.fn()]);

const title = () => screen.getByLabelText(/^Title/);
const amount = () => screen.getByLabelText(/^Amount/);
const currency = () => screen.getByLabelText(/^Currency/);
const probability = () => screen.getByLabelText(/^Probability/);
const expectedClose = () => screen.getByLabelText(/^Expected close/);
const stageSelect = () => screen.getByRole('combobox', { name: /^Stage/ });
const submit = (label: RegExp) => fireEvent.click(screen.getByRole('button', { name: label }));

const pickStage = async (label: string) => {
  fireEvent.mouseDown(stageSelect());
  fireEvent.click(await screen.findByRole('option', { name: label }));
};

const baseBody = {
  title: 'Website redesign',
  stage: 'qualification',
  amount_cents: 0,
  currency: 'EUR',
  probability: 10,
  expected_close_date: null,
  lost_reason: '',
  source: '',
  notes: '',
};

describe('DealForm', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    setSearch('');
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
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
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies: [acme], total: 1 });
    vi.mocked(companiesApi.getCompany).mockResolvedValue(acme);
    vi.mocked(customersApi.getCustomers).mockResolvedValue({ data: [jane], total: 1, page: 1, limit: 20, total_pages: 1 });
    vi.mocked(customersApi.getCustomer).mockResolvedValue(jane);
    vi.mocked(leadsApi.getLeads).mockResolvedValue({ data: [ion], total: 1, page: 1, limit: 20, total_pages: 1 });
    vi.mocked(leadsApi.getLead).mockResolvedValue(ion);
    vi.mocked(dealsApi.createDeal).mockResolvedValue(createMockDeal({ id: 42 }));
  });

  describe('create', () => {
    it('renders the fields with the stage defaults and the owner picker for an admin', async () => {
      render(<DealForm />);

      expect(screen.getByRole('heading', { name: 'Create New Deal' })).toBeInTheDocument();
      expect(title()).toBeInTheDocument();
      expect(stageSelect()).toHaveTextContent('Qualification');
      expect(probability()).toHaveValue(10);
      expect(currency()).toHaveValue('EUR');
      expect(amount()).toBeInTheDocument();
      expect(expectedClose()).toBeInTheDocument();
      expect(screen.getByLabelText('Company')).toBeInTheDocument();
      expect(screen.getByLabelText('Customer')).toBeInTheDocument();
      expect(screen.getByLabelText('Lead')).toBeInTheDocument();
      expect(screen.getByLabelText(/^Owner/)).toBeInTheDocument();
      // The lost reason only matters in the lost stage.
      expect(screen.queryByLabelText(/^Lost reason/)).not.toBeInTheDocument();
      await waitFor(() => expect(usersApi.getUsers).toHaveBeenCalledWith({ is_active: true }));
    });

    it('takes the configured default currency until the user edits it', async () => {
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([currencyConfig('usd')]);
      render(<DealForm />);

      await waitFor(() => expect(currency()).toHaveValue('USD'));
    });

    it('keeps the typed currency when the configuration arrives later', async () => {
      let resolveConfig: (value: Configuration[]) => void = () => {};
      vi.mocked(configurationsApi.getUIConfigurations).mockReturnValue(
        new Promise((resolve) => {
          resolveConfig = resolve;
        })
      );
      render(<DealForm />);

      fireEvent.change(currency(), { target: { value: 'ron' } });
      expect(currency()).toHaveValue('RON');
      resolveConfig([currencyConfig('USD')]);

      await waitFor(() => expect(configurationsApi.getUIConfigurations).toHaveBeenCalled());
      expect(currency()).toHaveValue('RON');
    });

    it('requires a title and refuses a malformed amount, currency and probability', async () => {
      render(<DealForm />);

      fireEvent.change(amount(), { target: { value: '12,5' } });
      fireEvent.change(currency(), { target: { value: 'EU' } });
      fireEvent.change(probability(), { target: { value: '150' } });
      submit(/Create Deal/);

      expect(await screen.findByText('Title is required')).toBeInTheDocument();
      expect(screen.getByText('Amount must be a positive number with at most two decimals')).toBeInTheDocument();
      expect(screen.getByText('Currency must be a 3-letter ISO code')).toBeInTheDocument();
      expect(screen.getByText('Probability must be a whole number between 0 and 100')).toBeInTheDocument();
      expect(dealsApi.createDeal).not.toHaveBeenCalled();
    });

    it('enforces the column bounds', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'x'.repeat(201) } });
      fireEvent.change(screen.getByLabelText(/^Source/), { target: { value: 'y'.repeat(101) } });
      submit(/Create Deal/);

      expect(await screen.findByText('Title must be 200 characters or fewer')).toBeInTheDocument();
      expect(screen.getByText('Source must be 100 characters or fewer')).toBeInTheDocument();
      expect(dealsApi.createDeal).not.toHaveBeenCalled();
    });

    it('refuses an amount above the API bound of 9007199254740991 cents and accepts the bound', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      fireEvent.change(amount(), { target: { value: '90071992547409.92' } });
      submit(/Create Deal/);

      expect(await screen.findByText('Amount is too large')).toBeInTheDocument();
      expect(dealsApi.createDeal).not.toHaveBeenCalled();

      fireEvent.change(amount(), { target: { value: '90071992547409.91' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith({ ...baseBody, amount_cents: 9007199254740991 });
    });

    it('fills the probability from the stage until the user overrides it', async () => {
      render(<DealForm />);

      await pickStage('Proposal');
      expect(probability()).toHaveValue(40);
      await pickStage('Negotiation');
      expect(probability()).toHaveValue(70);

      fireEvent.change(probability(), { target: { value: '55' } });
      await pickStage('Won');
      expect(probability()).toHaveValue(55);
    });

    it('marks the dashboard pipeline stale after a create', async () => {
      const client = clientWithDashboardPipeline();
      render(withClient(client, <DealForm />));

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      await waitFor(() => expect(isInvalidated(client, DASHBOARD_PIPELINE_KEY)).toBe(true));
    });

    it('converts the decimal amount to cents and sends the date as YYYY-MM-DD', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: '  Website redesign  ' } });
      fireEvent.change(amount(), { target: { value: '1234.5' } });
      fireEvent.change(expectedClose(), { target: { value: '2026-12-15' } });
      await pickStage('Proposal');
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith({
        ...baseBody,
        stage: 'proposal',
        probability: 40,
        amount_cents: 123450,
        expected_close_date: '2026-12-15',
      });
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/deals/42'), { timeout: 3000 });
      expect(showSuccess).toHaveBeenCalledWith('Deal created successfully');
    });

    it('sends the ids of the picked company, customer and lead', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      await pickAutocompleteOption('Company', 'Acme Widgets (acme.example)');
      await pickAutocompleteOption('Customer', 'Jane Smith (Acme Widgets)');
      await pickAutocompleteOption('Lead', 'Ion Ionescu (Bolt Robotics)');
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith({
        ...baseBody,
        company_id: 7,
        customer_id: 3,
        lead_id: 5,
      });
    });

    it('prefills the company from ?company_id= and sends it', async () => {
      setSearch('company_id=7');
      render(<DealForm />);

      await waitFor(() => expect(companiesApi.getCompany).toHaveBeenCalledWith(7));
      await waitFor(() => expect(screen.getByLabelText('Company')).toHaveValue('Acme Widgets (acme.example)'));
      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith(expect.objectContaining({ company_id: 7 }));
    });

    it('prefills the customer from ?customer_id= and sends it', async () => {
      setSearch('customer_id=3');
      render(<DealForm />);

      await waitFor(() => expect(customersApi.getCustomer).toHaveBeenCalledWith(3));
      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith(expect.objectContaining({ customer_id: 3 }));
      expect(dealsApi.createDeal).not.toHaveBeenCalledWith(expect.objectContaining({ company_id: expect.anything() }));
    });

    it('puts an INVALID_REFERENCE message on the picker it names', async () => {
      const message = 'unknown customer_id 3: customer not found';
      vi.mocked(dealsApi.createDeal).mockRejectedValue(apiError(400, 'INVALID_REFERENCE', message));
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      await pickAutocompleteOption('Customer', 'Jane Smith (Acme Widgets)');
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      const helper = await screen.findByText(message, undefined, { timeout: 3000 });
      // The helper line belongs to the customer picker, not the company one.
      expect(helper.closest('.MuiFormControl-root')).toContainElement(screen.getByLabelText('Customer'));
      expect(showError).toHaveBeenCalledWith(message);
    });

    it('shows the 403 message when the owner rule refuses the request', async () => {
      const message = 'only an admin may assign another owner';
      vi.mocked(dealsApi.createDeal).mockRejectedValue(apiError(403, 'FORBIDDEN', message));
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      await waitFor(() => expect(showError).toHaveBeenCalledWith(message), { timeout: 3000 });
    });

    it('reports other failures generically', async () => {
      vi.mocked(dealsApi.createDeal).mockRejectedValue(new Error('network'));
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(showError).toHaveBeenCalledWith('Failed to create deal'), { timeout: 3000 });
    });

    it('sends the picked owner for an admin', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      await pickAutocompleteOption(/^Owner/, 'Ion Ionescu');
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith(expect.objectContaining({ owner_id: 2 }));
    });

    it('hides the owner picker for sales and never sends owner_id', async () => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
      render(<DealForm />);

      expect(screen.queryByLabelText(/^Owner/)).not.toBeInTheDocument();
      expect(usersApi.getUsers).not.toHaveBeenCalled();
      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith(baseBody);
    });

    it('asks for the lost reason in the lost stage and sends it', async () => {
      render(<DealForm />);

      fireEvent.change(title(), { target: { value: 'Website redesign' } });
      await pickStage('Lost');
      expect(probability()).toHaveValue(0);
      fireEvent.change(screen.getByLabelText(/^Lost reason/), { target: { value: 'Budget cut' } });
      submit(/Create Deal/);

      await waitFor(() => expect(dealsApi.createDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.createDeal).toHaveBeenCalledWith({
        ...baseBody,
        stage: 'lost',
        probability: 0,
        lost_reason: 'Budget cut',
      });
    });

    it('returns to the list on cancel', () => {
      render(<DealForm />);

      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

      expect(mockNavigate).toHaveBeenCalledWith('/deals');
    });
  });

  describe('edit', () => {
    const existing = createMockDeal({
      id: 9,
      title: 'Website redesign',
      stage: 'proposal',
      amount_cents: 1250000,
      currency: 'USD',
      probability: 45,
      expected_close_date: '2026-12-15',
      source: 'referral',
      notes: 'Long standing',
      company_id: 7,
      company: acme,
      owner: createMockUser({ id: 2, first_name: 'Ion', last_name: 'Ionescu' }),
    });

    beforeEach(() => {
      vi.mocked(useParams).mockReturnValue({ id: '9' });
      vi.mocked(dealsApi.getDeal).mockResolvedValue(existing);
      vi.mocked(dealsApi.updateDeal).mockResolvedValue(existing);
    });

    it('loads the deal into the form without touching the configured currency', async () => {
      render(<DealForm />);

      expect(await screen.findByRole('heading', { name: 'Edit Deal' })).toBeInTheDocument();
      await waitFor(() => expect(title()).toHaveValue('Website redesign'));
      expect(amount()).toHaveValue('12500.00');
      expect(currency()).toHaveValue('USD');
      expect(probability()).toHaveValue(45);
      expect(expectedClose()).toHaveValue('2026-12-15');
      expect(stageSelect()).toHaveTextContent('Proposal');
      expect(screen.getByLabelText('Company')).toHaveValue('Acme Widgets (acme.example)');
      expect(screen.getByLabelText(/^Owner/)).toHaveValue('Ion Ionescu');
      expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
    });

    it('PUTs the full body, keeps the company and omits an untouched owner', async () => {
      render(<DealForm />);
      await waitFor(() => expect(title()).toHaveValue('Website redesign'));

      fireEvent.change(title(), { target: { value: 'Website redesign v2' } });
      fireEvent.change(amount(), { target: { value: '99.99' } });
      submit(/Update Deal/);

      await waitFor(() => expect(dealsApi.updateDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.updateDeal).toHaveBeenCalledWith(9, {
        title: 'Website redesign v2',
        stage: 'proposal',
        amount_cents: 9999,
        currency: 'USD',
        probability: 45,
        expected_close_date: '2026-12-15',
        lost_reason: '',
        source: 'referral',
        notes: 'Long standing',
        company_id: 7,
      });
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/deals/9'), { timeout: 3000 });
    });

    it('sends company_id 0 when the linked company is cleared', async () => {
      render(<DealForm />);
      await waitFor(() => expect(screen.getByLabelText('Company')).toHaveValue('Acme Widgets (acme.example)'));

      const companyControl = screen.getByLabelText('Company').closest('.MuiAutocomplete-root') as HTMLElement;
      fireEvent.click(companyControl.querySelector('button[aria-label="Clear"]') as HTMLElement);
      await waitFor(() => expect(screen.getByLabelText('Company')).toHaveValue(''), { timeout: 3000 });
      submit(/Update Deal/);

      await waitFor(() => expect(dealsApi.updateDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
      expect(dealsApi.updateDeal).toHaveBeenCalledWith(9, expect.objectContaining({ company_id: 0 }));
    });

    it('takes the new stage default when the loaded probability was not edited', async () => {
      render(<DealForm />);
      await waitFor(() => expect(probability()).toHaveValue(45));

      await pickStage('Negotiation');
      expect(probability()).toHaveValue(70);
    });

    it('keeps the edited probability when the stage changes afterwards', async () => {
      render(<DealForm />);
      await waitFor(() => expect(probability()).toHaveValue(45));

      fireEvent.change(probability(), { target: { value: '33' } });
      await pickStage('Negotiation');
      expect(probability()).toHaveValue(33);
    });

    // The API preloads live records only, so a deal linked to an erased
    // customer arrives with customer_id and no customer.
    describe('a link whose record is gone', () => {
      const missingHelper = 'Linked record no longer available; pick another to replace it';

      beforeEach(() => {
        vi.mocked(dealsApi.getDeal).mockResolvedValue({ ...existing, customer_id: 30, customer: undefined });
      });

      it('leaves the picker empty, says so, and does not resend the stale id', async () => {
        render(<DealForm />);
        await waitFor(() => expect(title()).toHaveValue('Website redesign'));

        expect(screen.getByLabelText('Customer')).toHaveValue('');
        expect(screen.getByText(missingHelper)).toBeInTheDocument();
        expect(customersApi.getCustomer).not.toHaveBeenCalled();
        submit(/Update Deal/);

        await waitFor(() => expect(dealsApi.updateDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
        const body = vi.mocked(dealsApi.updateDeal).mock.calls[0][1];
        expect(body).not.toHaveProperty('customer_id');
        expect(body).toMatchObject({ title: 'Website redesign', company_id: 7 });
      });

      it('sends the newly picked customer in its place', async () => {
        render(<DealForm />);
        await waitFor(() => expect(title()).toHaveValue('Website redesign'));

        await pickAutocompleteOption('Customer', 'Jane Smith (Acme Widgets)');
        expect(screen.queryByText(missingHelper)).not.toBeInTheDocument();
        submit(/Update Deal/);

        await waitFor(() => expect(dealsApi.updateDeal).toHaveBeenCalledTimes(1), { timeout: 3000 });
        expect(dealsApi.updateDeal).toHaveBeenCalledWith(9, expect.objectContaining({ customer_id: 3 }));
      });
    });

    it('cancel returns to the detail page', async () => {
      render(<DealForm />);
      await waitFor(() => expect(title()).toHaveValue('Website redesign'));

      fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));

      expect(mockNavigate).toHaveBeenCalledWith('/deals/9');
    });
  });
});
