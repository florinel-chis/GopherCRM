import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@/test/test-utils';
import { Component as FormDetail } from './FormDetail';
import { formsApi } from '@/api/endpoints/forms';
import { createMockUser } from '@/test/factories';

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({ useAuth: () => mockUseAuth() }));
vi.mock('@/hooks/useSnackbar', () => ({ useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn(), showWarning: vi.fn(), showInfo: vi.fn() }) }));
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => vi.fn(), useParams: () => ({ id: '1' }) };
});
vi.mock('@/api/endpoints/forms', () => ({ formsApi: { get: vi.fn(), listSubmissions: vi.fn(), delete: vi.fn() } }));

describe('FormDetail', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue({ user: createMockUser({ id: 1, role: 'admin' }), isLoading: false, isAuthenticated: true, login: vi.fn(), register: vi.fn(), logout: vi.fn(), refreshUser: vi.fn() });
    (formsApi.get as any).mockResolvedValue({
      id: 1, name: 'Contact us', description: 'desc', public_id: 'pub123', status: 'published',
      fields: [{ name: 'email', label: 'Email', type: 'email', required: true }],
      submit_action: 'message', thank_you_message: '', redirect_url: '', consent_text: '',
      notify_emails: ['sales@example.com'], double_opt_in: true, confirmation_subject: '', confirmation_body: '',
      follow_up_subject: '', follow_up_body: '', content_url: '', captcha_enabled: false, create_lead: true,
      default_owner_id: 1, allowed_domains: [], created_at: '2026-08-01T10:00:00Z', updated_at: '2026-08-01T10:00:00Z',
    });
    (formsApi.listSubmissions as any).mockResolvedValue({
      submissions: [{ id: 5, form_id: 1, data: { email: 'a@b.com', message: 'hi' }, email: 'a@b.com', status: 'confirmed', spam_reason: '', lead_id: 9, ip_address: '1.2.3.4', user_agent: 'ua', referrer: 'https://site', confirmed_at: '2026-08-02T10:00:00Z', created_at: '2026-08-02T09:00:00Z' }],
      total: 1,
    });
  });

  // The snippet is what an admin pastes into a foreign site: its shape and the
  // public id it carries are the whole point of this page.
  it('renders the embed snippet, hosted link and submissions', async () => {
    render(<FormDetail />);
    expect(await screen.findByText('Contact us')).toBeInTheDocument();
    expect(screen.getByTestId('embed-snippet').textContent).toContain('data-form-key="pub123"');
    expect(screen.getByTestId('embed-snippet').textContent).toContain('/forms/public/embed.js');
    expect(screen.getByText(/\/forms\/public\/pub123\/view$/)).toBeInTheDocument();
    expect(await screen.findByText('a@b.com')).toBeInTheDocument();
    expect(screen.getByText('confirmed')).toBeInTheDocument();
    fireEvent.click(screen.getByText('a@b.com'));
    await waitFor(() => expect(screen.getByText('Submission #5')).toBeInTheDocument());
    expect(screen.getByText('hi')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /open lead #9/i })).toBeInTheDocument();
  });
});
