import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { SignInAudit } from './SignInAudit';
import { signInAuditApi, type SignInAuditEntry } from '@/api/endpoints';
import { formatDate, localDateTimeToUTC } from '@/utils/date';

vi.mock('@/api/endpoints', async () => {
  const actual = await vi.importActual<typeof import('@/api/endpoints/signInAudit')>(
    '@/api/endpoints/signInAudit',
  );
  return {
    SIGN_IN_AUDIT_METHODS: actual.SIGN_IN_AUDIT_METHODS,
    SIGN_IN_AUDIT_OUTCOMES: actual.SIGN_IN_AUDIT_OUTCOMES,
    SIGN_IN_AUDIT_REASONS: actual.SIGN_IN_AUDIT_REASONS,
    signInAuditApi: { list: vi.fn() },
  };
});

const entry = (overrides: Partial<SignInAuditEntry>): SignInAuditEntry => ({
  id: 1,
  created_at: '2026-11-20T09:30:00Z',
  method: 'password',
  outcome: 'success',
  reason: '',
  user_id: 1,
  attempt_id: null,
  request_id: '',
  subject_ref: null,
  claim_roles: null,
  claim_groups: null,
  client_network: '192.0.2.0/24',
  user_agent_family: 'Chrome on Linux',
  ...overrides,
});

const rows: SignInAuditEntry[] = [
  entry({
    id: 9,
    created_at: '2026-11-21T12:00:05Z',
    method: 'oidc',
    outcome: 'refused',
    reason: 'no_mapping_match',
    user_id: null,
    request_id: 'req-7f3a',
    subject_ref: '9b74c9897bac770ffc029102a200c5de',
    user_agent_family: 'Firefox on Linux',
  }),
  entry({ id: 4 }),
];

const lastParams = () => vi.mocked(signInAuditApi.list).mock.calls.at(-1)?.[0];

const pickSelect = async (name: string, option: string) => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name }));
  fireEvent.click(await screen.findByRole('option', { name: option }));
};

describe('SignInAudit', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(signInAuditApi.list).mockResolvedValue({ rows, total: 45 });
  });

  it('shows the rows in the order the API returned, the time through formatDate', async () => {
    render(<SignInAudit />);
    await screen.findByText('no_mapping_match');

    const cells = screen.getAllByTestId('sign-in-audit-row').map((row) =>
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    );
    expect(cells).toEqual([
      [
        formatDate('2026-11-21T12:00:05Z', 'PPpp'),
        'oidc',
        'refused',
        'no_mapping_match',
        '-',
        '192.0.2.0/24',
        'Firefox on Linux',
        'req-7f3a',
        '9b74c9897bac770ffc029102a200c5de',
      ],
      [
        formatDate('2026-11-20T09:30:00Z', 'PPpp'),
        'password',
        'success',
        '-',
        '1',
        '192.0.2.0/24',
        'Chrome on Linux',
        '-',
        '-',
      ],
    ]);
    expect(screen.getByText(/of 45/)).toBeInTheDocument();
    expect(lastParams()).toEqual({
      method: undefined,
      outcome: undefined,
      reason: undefined,
      user_id: undefined,
      from: undefined,
      to: undefined,
      offset: 0,
      limit: 20,
    });
  });

  it('sends exactly the chosen filters and goes back to offset 0', async () => {
    render(<SignInAudit />);
    await screen.findByText('no_mapping_match');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(lastParams()).toMatchObject({ offset: 20 }));

    await pickSelect('Outcome', 'refused');
    await pickSelect('Reason', 'no_mapping_match');
    await waitFor(() =>
      expect(lastParams()).toEqual({
        method: undefined,
        outcome: 'refused',
        reason: 'no_mapping_match',
        user_id: undefined,
        from: undefined,
        to: undefined,
        offset: 0,
        limit: 20,
      }),
    );
  });

  it('sends the user id and the range as UTC instants', async () => {
    render(<SignInAudit />);
    await screen.findByText('no_mapping_match');

    fireEvent.change(screen.getByLabelText('User id'), { target: { value: '7' } });
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '2026-11-20T09:30' } });
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '2026-11-21T09:30' } });
    await waitFor(() =>
      expect(lastParams()).toMatchObject({
        user_id: 7,
        from: localDateTimeToUTC('2026-11-20T09:30'),
        to: localDateTimeToUTC('2026-11-21T09:30'),
        offset: 0,
      }),
    );
  });

  it('shows the empty state for an empty result', async () => {
    vi.mocked(signInAuditApi.list).mockResolvedValue({ rows: [], total: 0 });
    render(<SignInAudit />);
    expect(await screen.findByText('No sign-ins match these filters.')).toBeInTheDocument();
  });

  it('shows the API error', async () => {
    vi.mocked(signInAuditApi.list).mockRejectedValue({
      response: { status: 400, data: { message: 'Validation failed' } },
    });
    render(<SignInAudit />);
    expect(await screen.findByText('Validation failed')).toBeInTheDocument();
  });
});
