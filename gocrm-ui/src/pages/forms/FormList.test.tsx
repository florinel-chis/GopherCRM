import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as FormList } from './FormList';
import { formsApi, type Form } from '@/api/endpoints/forms';
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
  return { ...actual, useNavigate: () => mockNavigate };
});

vi.mock('@/api/endpoints/forms', () => ({
  formsApi: {
    list: vi.fn(),
    delete: vi.fn(),
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

const baseForm = (overrides?: Partial<Form>): Form => ({
  id: 1,
  name: 'Contact us',
  description: 'Front-page contact form',
  public_id: 'abcdef0123456789',
  status: 'published',
  fields: [{ name: 'email', label: 'Email', type: 'email', required: true }],
  submit_action: 'message',
  thank_you_message: '',
  redirect_url: '',
  consent_text: '',
  notify_emails: [],
  double_opt_in: false,
  confirmation_subject: '',
  confirmation_body: '',
  follow_up_subject: '',
  follow_up_body: '',
  content_url: '',
  captcha_enabled: false,
  create_lead: true,
  default_owner_id: 1,
  allowed_domains: [],
  created_at: '2026-08-01T10:00:00Z',
  updated_at: '2026-08-01T10:00:00Z',
  submission_count: 12,
  ...overrides,
});

const forms = [
  baseForm(),
  baseForm({ id: 2, name: 'Whitepaper download', status: 'draft', submission_count: 0 }),
];

describe('FormList', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    (formsApi.list as any).mockResolvedValue({ forms, total: 2 });
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows the loading state while the first page is fetched', () => {
    (formsApi.list as any).mockImplementation(() => new Promise(() => {}));

    render(<FormList />);

    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('renders a row per form with its status chip and submission count', async () => {
    render(<FormList />);

    expect(await screen.findByText('Contact us')).toBeInTheDocument();

    const publishedRow = screen.getByText('Contact us').closest('tr') as HTMLElement;
    expect(within(publishedRow).getByText('Published')).toBeInTheDocument();
    expect(within(publishedRow).getByText('12')).toBeInTheDocument();
    expect(within(publishedRow).getByText('Aug 01, 2026')).toBeInTheDocument();

    const draftRow = screen.getByText('Whitepaper download').closest('tr') as HTMLElement;
    expect(within(draftRow).getByText('Draft')).toBeInTheDocument();
    expect(within(draftRow).getByText('0')).toBeInTheDocument();
  });

  it('requests the first page with the default paging window', async () => {
    render(<FormList />);

    await waitFor(() => {
      expect(formsApi.list).toHaveBeenCalledWith({
        offset: 0,
        limit: 10,
        status: undefined,
      });
    });
  });

  it('refetches with the selected status filter', async () => {
    render(<FormList />);

    await screen.findByText('Contact us');

    fireEvent.mouseDown(screen.getByLabelText('Status'));
    fireEvent.click(within(screen.getByRole('listbox')).getByText('Published'));

    await waitFor(() => {
      expect(formsApi.list).toHaveBeenLastCalledWith({
        offset: 0,
        limit: 10,
        status: 'published',
      });
    });
  });

  it('opens the detail page when a row is clicked', async () => {
    render(<FormList />);

    fireEvent.click(await screen.findByText('Contact us'));

    expect(mockNavigate).toHaveBeenCalledWith('/forms/1');
  });

  it('deletes a form after the confirmation dialog', async () => {
    (formsApi.delete as any).mockResolvedValue(undefined);

    render(<FormList />);

    await screen.findByText('Contact us');
    const row = screen.getByText('Contact us').closest('tr') as HTMLElement;
    // The trailing action cell holds view, edit and delete, in that order.
    const buttons = within(row).getAllByRole('button');
    fireEvent.click(buttons[buttons.length - 1]);

    expect(await screen.findByText(/Delete the form "Contact us"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(formsApi.delete).toHaveBeenCalledWith(1);
    });
    await waitFor(() => {
      expect(showSuccess).toHaveBeenCalledWith('Form deleted successfully');
    });
  });

  it.each(['admin', 'sales'] as const)('offers the create button to the %s role', async (role) => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));

    render(<FormList />);

    await screen.findByText('Contact us');
    expect(screen.getByRole('button', { name: /create form/i })).toBeInTheDocument();
  });

  it('renders read-only for the support role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));

    render(<FormList />);

    await screen.findByText('Contact us');

    expect(screen.queryByRole('button', { name: /create form/i })).not.toBeInTheDocument();
    // View is the only row action left: no edit pencil, no delete bin.
    const row = screen.getByText('Contact us').closest('tr') as HTMLElement;
    expect(within(row).getAllByRole('button')).toHaveLength(1);
  });

  it('hides the delete action from the sales role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));

    render(<FormList />);

    await screen.findByText('Contact us');

    const row = screen.getByText('Contact us').closest('tr') as HTMLElement;
    expect(within(row).getAllByRole('button')).toHaveLength(2);
  });
});
