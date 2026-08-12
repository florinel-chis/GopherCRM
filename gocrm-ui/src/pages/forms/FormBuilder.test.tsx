import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as FormBuilder } from './FormBuilder';
import { formsApi } from '@/api/endpoints/forms';
import { usersApi } from '@/api/endpoints/users';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();
const mockNavigate = vi.fn();
const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => mockNavigate, useParams: () => ({}) };
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

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

// Adds a field of the given type through the "Add field" menu.
const addField = (type: string) => {
  fireEvent.click(screen.getByRole('button', { name: /add field/i }));
  fireEvent.click(within(screen.getByRole('menu')).getByText(type));
};

const emailField = {
  name: 'email',
  label: 'Email',
  type: 'email',
  required: true,
  placeholder: '',
  help_text: '',
  options: undefined,
  max_length: 1000,
};

describe('FormBuilder', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // A sales user owns the leads its own forms create, so the owner select
    // needs no user list.
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 7, role: 'sales' })));
    (usersApi.getUsers as any).mockResolvedValue({
      data: [createMockUser({ id: 1, role: 'admin', email: 'admin@example.com' })],
      total: 1,
      page: 1,
      limit: 100,
      total_pages: 1,
    });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('starts a new definition with the email field the server insists on', () => {
    render(<FormBuilder />);

    expect(screen.getByRole('heading', { name: 'Create Form' })).toBeInTheDocument();
    expect(screen.getByTestId('field-editor-0')).toBeInTheDocument();
    expect(screen.getByLabelText(/^Field 1 name/)).toHaveValue('email');
  });

  it('renders the editors for a newly added field', () => {
    render(<FormBuilder />);

    addField('Dropdown');

    const editor = screen.getByTestId('field-editor-1');
    expect(within(editor).getByLabelText(/^Field 2 label/)).toBeInTheDocument();
    expect(within(editor).getByLabelText(/^Field 2 name/)).toBeInTheDocument();
    expect(within(editor).getByLabelText(/^Field 2 options/)).toBeInTheDocument();
    expect(within(editor).getByLabelText(/^Field 2 placeholder/)).toBeInTheDocument();
  });

  it('suggests the machine name from the label', () => {
    render(<FormBuilder />);

    addField('Long text');
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), {
      target: { value: 'Your message' },
    });

    expect(screen.getByLabelText(/^Field 2 name/)).toHaveValue('your_message');
  });

  it('swaps two fields with the reorder buttons', () => {
    render(<FormBuilder />);

    addField('Long text');
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), { target: { value: 'Message' } });
    expect(screen.getByLabelText(/^Field 2 name/)).toHaveValue('message');

    fireEvent.click(screen.getByRole('button', { name: 'Move field 2 up' }));

    expect(screen.getByLabelText(/^Field 1 name/)).toHaveValue('message');
    expect(screen.getByLabelText(/^Field 2 name/)).toHaveValue('email');
  });

  it('refuses to save a definition without an email field', async () => {
    render(<FormBuilder />);

    addField('Text');
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Contact us' } });
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), { target: { value: 'Company' } });
    fireEvent.click(screen.getByRole('button', { name: 'Remove field 1' }));
    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    expect(await screen.findByText(/Add exactly one email field/)).toBeInTheDocument();
    expect(formsApi.create).not.toHaveBeenCalled();
  });

  it('refuses to save a redirect without a URL', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Contact us' } });
    fireEvent.click(screen.getByLabelText('Redirect to a URL'));
    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    expect(await screen.findByText('A redirect needs an http(s) URL')).toBeInTheDocument();
    expect(formsApi.create).not.toHaveBeenCalled();
  });

  it('refuses a notification address that is not an email', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Contact us' } });
    fireEvent.change(screen.getByLabelText('Notification recipients'), {
      target: { value: 'not-an-address' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add Notification recipients' }));
    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    expect(
      await screen.findByText('"not-an-address" is not a valid email address')
    ).toBeInTheDocument();
    expect(formsApi.create).not.toHaveBeenCalled();
  });

  it('sends a snake_case payload with the field definitions', async () => {
    (formsApi.create as any).mockResolvedValue({ id: 42 });

    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Contact us' } });
    addField('Long text');
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), { target: { value: 'Message' } });
    fireEvent.change(screen.getByLabelText('Notification recipients'), {
      target: { value: 'sales@example.com' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add Notification recipients' }));

    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    await waitFor(() => {
      expect(formsApi.create).toHaveBeenCalledWith({
        name: 'Contact us',
        description: '',
        status: 'draft',
        fields: [
          emailField,
          {
            name: 'message',
            label: 'Message',
            type: 'textarea',
            required: false,
            placeholder: '',
            help_text: '',
            options: undefined,
            max_length: 5000,
          },
        ],
        submit_action: 'message',
        thank_you_message: '',
        redirect_url: '',
        consent_text: '',
        notify_emails: ['sales@example.com'],
        double_opt_in: false,
        confirmation_subject: '',
        confirmation_body: '',
        follow_up_subject: '',
        follow_up_body: '',
        content_url: '',
        captcha_enabled: false,
        create_lead: true,
        // Filled in from the signed-in sales user, who cannot list users.
        default_owner_id: 7,
        allowed_domains: [],
      });
    });

    await waitFor(() => {
      expect(mockNavigate).toHaveBeenCalledWith('/forms/42');
    });
  });

  it('keeps the dropdown options and drops them from the other fields', async () => {
    (formsApi.create as any).mockResolvedValue({ id: 43 });

    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Quote request' } });
    addField('Dropdown');
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), { target: { value: 'Budget' } });
    fireEvent.change(screen.getByLabelText(/^Field 2 options/), { target: { value: 'Under 10k' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add Field 2 options' }));

    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    await waitFor(() => {
      expect(formsApi.create).toHaveBeenCalledWith(
        expect.objectContaining({
          fields: [
            emailField,
            expect.objectContaining({
              name: 'budget',
              type: 'select',
              options: ['Under 10k'],
            }),
          ],
        })
      );
    });
  });

  it('refuses a dropdown without options', async () => {
    render(<FormBuilder />);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Quote request' } });
    addField('Dropdown');
    fireEvent.change(screen.getByLabelText(/^Field 2 label/), { target: { value: 'Budget' } });

    fireEvent.click(screen.getByRole('button', { name: /create form/i }));

    expect(await screen.findByText('A dropdown needs at least one option')).toBeInTheDocument();
    expect(formsApi.create).not.toHaveBeenCalled();
  });

  it('lets an admin pick the lead owner from the user list', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));

    render(<FormBuilder />);

    await waitFor(() => {
      expect(usersApi.getUsers).toHaveBeenCalledWith({ limit: 100 });
    });

    fireEvent.mouseDown(screen.getByLabelText(/^Lead owner/));

    expect(await screen.findByText(/admin@example.com/)).toBeInTheDocument();
    expect(screen.getByRole('listbox')).toBeInTheDocument();
  });

  it('renders read-only for the support role', () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));

    render(<FormBuilder />);

    expect(screen.getByText(/read-only access to forms/i)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^create form$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /add field/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove field 1' })).not.toBeInTheDocument();
    expect(screen.getByLabelText(/^Name/)).toBeDisabled();
  });
});
