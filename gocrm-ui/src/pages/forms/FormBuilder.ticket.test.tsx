import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as FormBuilder } from './FormBuilder';
import { formsApi, type Form } from '@/api/endpoints/forms';
import { usersApi } from '@/api/endpoints/users';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

// The submission action of the builder (#335): one choice between a lead, a
// support ticket (#213) and nothing, holding the server's rules — a ticket form
// needs double opt-in, creates no lead and has no lead owner.

const mockUseAuth = vi.fn();
const mockUseParams = vi.fn();
const mockNavigate = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
}));

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => mockNavigate, useParams: () => mockUseParams() };
});

vi.mock('@/api/endpoints/forms', () => ({
  formsApi: {
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
  },
}));

vi.mock('@/api/endpoints/users', () => ({
  usersApi: {
    getUsers: vi.fn(),
  },
}));

const DOUBLE_OPT_IN_LABEL = 'Require email confirmation before a lead or ticket is created';
const REPEAT_LEAD_HELPER =
  /A submission from a known email address is appended to the existing lead/;
const TICKET_HELPER = /A confirmed submission opens a ticket for the live customer/;

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const storedForm = (overrides?: Partial<Form>): Form => ({
  id: 5,
  name: 'Support request',
  description: '',
  public_id: 'form-5',
  status: 'published',
  fields: [{ name: 'email', label: 'Email', type: 'email', required: true, max_length: 1000 }],
  submit_action: 'message',
  thank_you_message: '',
  redirect_url: '',
  consent_text: '',
  notify_emails: [],
  double_opt_in: true,
  confirmation_subject: '',
  confirmation_body: '',
  follow_up_subject: '',
  follow_up_body: '',
  content_url: '',
  captcha_enabled: false,
  create_lead: false,
  create_ticket: true,
  default_owner_id: 0,
  allowed_domains: [],
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
  ...overrides,
});

const radio = (name: string) => screen.getByRole('radio', { name });
const doubleOptInSwitch = () => screen.getByLabelText(DOUBLE_OPT_IN_LABEL);

// Waits until the stored definition has replaced the empty defaults.
const waitForStoredForm = async (name: string) => {
  await waitFor(() => {
    expect(screen.getByLabelText(/^Name/)).toHaveValue(name);
  });
};

describe('FormBuilder ticket action', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseParams.mockReturnValue({});
    // A sales user is preset as the owner of a new form.
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 7, role: 'sales' })));
    vi.mocked(usersApi.getUsers).mockResolvedValue({
      data: [createMockUser({ id: 1, role: 'admin', email: 'admin@example.com' })],
      total: 1,
      page: 1,
      limit: 100,
      total_pages: 1,
    });
    vi.mocked(formsApi.create).mockImplementation(async (data) => ({
      ...data,
      id: 42,
      public_id: 'form-42',
      created_at: '2026-11-15T10:00:00Z',
      updated_at: '2026-11-15T10:00:00Z',
    }));
    vi.mocked(formsApi.update).mockImplementation(async (id, data) => ({
      ...data,
      id,
      public_id: `form-${id}`,
      created_at: '2026-11-01T10:00:00Z',
      updated_at: '2026-11-15T10:00:00Z',
    }));
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('offers lead, ticket and no action with lead selected for a new form', () => {
    render(<FormBuilder />);

    expect(screen.getByRole('heading', { name: 'Submission action' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Lead capture' })).not.toBeInTheDocument();

    const group = screen.getByRole('radiogroup', { name: 'Submission action' });
    const options = within(group).getAllByRole('radio');
    expect(options).toHaveLength(3);
    expect(radio('Create a lead')).toBeChecked();
    expect(radio('Create a support ticket')).not.toBeChecked();
    expect(radio('Create nothing')).not.toBeChecked();

    expect(screen.getByLabelText(/^Lead owner/)).toBeVisible();
    expect(screen.getByText(REPEAT_LEAD_HELPER)).toBeVisible();
    expect(screen.queryByText(TICKET_HELPER)).not.toBeInTheDocument();
  });

  it('selecting the ticket action switches double opt-in on and hides the lead owner', () => {
    render(<FormBuilder />);

    expect(doubleOptInSwitch()).not.toBeChecked();

    fireEvent.click(radio('Create a support ticket'));

    expect(radio('Create a support ticket')).toBeChecked();
    expect(radio('Create a lead')).not.toBeChecked();
    expect(doubleOptInSwitch()).toBeChecked();

    // Hidden with display:none, not unmounted.
    expect(screen.getByTestId('lead-action-settings')).toHaveStyle({ display: 'none' });
    const owner = screen.getByLabelText(/^Lead owner/);
    expect(owner).toBeInTheDocument();
    expect(owner).not.toBeVisible();
    const repeatLeadHelper = screen.getByText(REPEAT_LEAD_HELPER);
    expect(repeatLeadHelper).toBeInTheDocument();
    expect(repeatLeadHelper).not.toBeVisible();

    expect(screen.getByText(TICKET_HELPER)).toBeVisible();
  });

  it('sends create_ticket true with double opt-in and no lead owner', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Support request' } });
    fireEvent.click(radio('Create a support ticket'));
    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    await waitFor(() => {
      expect(formsApi.create).toHaveBeenCalledTimes(1);
    });
    expect(formsApi.create).toHaveBeenCalledWith(
      expect.objectContaining({
        create_ticket: true,
        create_lead: false,
        double_opt_in: true,
        // The sales user's preset id is not sent for a ticket form.
        default_owner_id: 0,
      })
    );
  });

  it('refuses to save a ticket form without double opt-in', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Support request' } });
    fireEvent.click(radio('Create a support ticket'));
    fireEvent.click(doubleOptInSwitch());
    expect(doubleOptInSwitch()).not.toBeChecked();
    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    const message = await screen.findByText('A form that creates tickets needs double opt-in');
    // FormSwitch renders the switch label and its error in one box.
    const switchBox = doubleOptInSwitch().closest('label')?.parentElement;
    expect(switchBox).toBeTruthy();
    expect(switchBox).toContainElement(message);
    expect(formsApi.create).not.toHaveBeenCalled();
  });

  it('refuses to save a stored form that both creates leads and creates tickets', async () => {
    mockUseParams.mockReturnValue({ id: '5' });
    vi.mocked(formsApi.get).mockResolvedValue(
      storedForm({ name: 'Mixed form', create_lead: true, create_ticket: true, default_owner_id: 7 })
    );

    render(<FormBuilder />);
    await waitForStoredForm('Mixed form');

    fireEvent.click(screen.getByRole('button', { name: 'Update Form' }));

    const message = await screen.findByText('A form cannot both create leads and create tickets');
    const group = screen.getByRole('radiogroup', { name: 'Submission action' });
    expect(group.parentElement).toContainElement(message);
    expect(formsApi.update).not.toHaveBeenCalled();
  });

  it('loads a stored ticket form and saves the flag unchanged', async () => {
    mockUseParams.mockReturnValue({ id: '5' });
    vi.mocked(formsApi.get).mockResolvedValue(storedForm());

    render(<FormBuilder />);
    await waitForStoredForm('Support request');

    expect(radio('Create a support ticket')).toBeChecked();
    expect(doubleOptInSwitch()).toBeChecked();

    fireEvent.click(screen.getByRole('button', { name: 'Update Form' }));

    await waitFor(() => {
      expect(formsApi.update).toHaveBeenCalledTimes(1);
    });
    expect(formsApi.update).toHaveBeenCalledWith(
      5,
      expect.objectContaining({
        create_ticket: true,
        create_lead: false,
        double_opt_in: true,
        default_owner_id: 0,
      })
    );
  });

  it('selecting no action sends create_lead and create_ticket false', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Newsletter' } });

    // From the default, no action leaves double opt-in off ...
    fireEvent.click(radio('Create nothing'));
    expect(radio('Create nothing')).toBeChecked();
    expect(doubleOptInSwitch()).not.toBeChecked();
    expect(screen.getByTestId('lead-action-settings')).toHaveStyle({ display: 'none' });

    // ... and after the ticket action turned it on, no action leaves it on.
    fireEvent.click(radio('Create a support ticket'));
    fireEvent.click(radio('Create nothing'));
    expect(doubleOptInSwitch()).toBeChecked();

    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    await waitFor(() => {
      expect(formsApi.create).toHaveBeenCalledTimes(1);
    });
    expect(formsApi.create).toHaveBeenCalledWith(
      expect.objectContaining({
        create_lead: false,
        create_ticket: false,
        default_owner_id: 0,
        double_opt_in: true,
      })
    );
  });

  it('disables the action choice for the support role', () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));

    render(<FormBuilder />);

    expect(radio('Create a lead')).toBeDisabled();
    expect(radio('Create a support ticket')).toBeDisabled();
    expect(radio('Create nothing')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Create Form' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Update Form' })).not.toBeInTheDocument();
  });

  it('labels double opt-in for leads and tickets alike', () => {
    render(<FormBuilder />);

    expect(doubleOptInSwitch()).toBeInTheDocument();
    expect(
      screen.queryByLabelText('Require email confirmation before the lead is created')
    ).not.toBeInTheDocument();
  });

  it('requires a lead owner when an edited ticket form switches to lead', async () => {
    mockUseParams.mockReturnValue({ id: '5' });
    vi.mocked(formsApi.get).mockResolvedValue(storedForm());

    render(<FormBuilder />);
    await waitForStoredForm('Support request');

    fireEvent.click(radio('Create a lead'));
    expect(screen.getByTestId('lead-action-settings')).toHaveStyle({ display: 'block' });
    fireEvent.click(screen.getByRole('button', { name: 'Update Form' }));

    const message = await screen.findByText('Choose the owner for the leads this form creates');
    const ownerControl = screen.getByLabelText(/^Lead owner/).closest('.MuiFormControl-root');
    expect(ownerControl).toBeTruthy();
    expect(ownerControl).toContainElement(message);
    expect(formsApi.update).not.toHaveBeenCalled();
  });
});
