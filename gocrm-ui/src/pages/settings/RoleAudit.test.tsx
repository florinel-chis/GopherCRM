import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { RoleAudit } from './RoleAudit';
import { roleAuditApi, type RoleAuditEntry } from '@/api/endpoints';
import { formatDate } from '@/utils/date';

vi.mock('@/api/endpoints', () => ({
  roleAuditApi: { list: vi.fn() },
}));

const entry = (overrides: Partial<RoleAuditEntry>): RoleAuditEntry => ({
  id: 1,
  created_at: '2026-11-20T09:30:00Z',
  actor_user_id: 1,
  auth_method: 'bearer',
  action: 'role_created',
  role_id: 3,
  role_name: 'Sales lead',
  target_user_id: null,
  before_state: null,
  after_state: null,
  ...overrides,
});

const rows: RoleAuditEntry[] = [
  entry({
    id: 12,
    created_at: '2026-11-21T12:00:12Z',
    action: 'user_erased',
    role_id: null,
    role_name: '',
    target_user_id: 9,
  }),
  entry({
    id: 11,
    created_at: '2026-11-21T12:00:11Z',
    action: 'user_activated',
    auth_method: 'api_key',
    role_id: null,
    role_name: '',
    target_user_id: 9,
  }),
  entry({
    id: 10,
    created_at: '2026-11-21T12:00:10Z',
    action: 'user_deactivated',
    auth_method: 'scim',
    actor_user_id: null,
    role_id: null,
    role_name: '',
    target_user_id: 9,
  }),
  entry({
    id: 9,
    created_at: '2026-11-21T12:00:09Z',
    action: 'user_email_changed',
    auth_method: 'oidc',
    role_id: null,
    role_name: '',
    target_user_id: 9,
  }),
  entry({
    id: 8,
    created_at: '2026-11-21T12:00:08Z',
    action: 'user_role_changed',
    auth_method: 'migration',
    actor_user_id: null,
    role_name: 'sales',
    target_user_id: 9,
  }),
  entry({ id: 7, created_at: '2026-11-21T12:00:07Z', action: 'role_updated' }),
  entry({ id: 6, created_at: '2026-11-21T12:00:06Z', action: 'role_deleted' }),
  entry({ id: 5, created_at: '2026-11-21T12:00:05Z', action: 'role_created' }),
  entry({
    id: 4,
    created_at: '2026-11-21T12:00:04Z',
    action: 'some_future_action',
    auth_method: 'future_method',
  }),
];

const lastParams = () => vi.mocked(roleAuditApi.list).mock.calls.at(-1)?.[0];

describe('RoleAudit', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(roleAuditApi.list).mockResolvedValue({ rows, total: 45 });
  });

  it('shows the rows in the order the API returned, with labelled actions and methods', async () => {
    render(<RoleAudit />);
    await screen.findByText('Account erased');

    const headers = screen.getAllByRole('columnheader').map((cell) => cell.textContent);
    expect(headers).toEqual(['Time', 'Action', 'Method', 'Acting user', 'Target user', 'Role']);

    const cells = screen.getAllByTestId('role-audit-row').map((row) =>
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    );
    expect(cells).toEqual([
      [formatDate('2026-11-21T12:00:12Z', 'PPpp'), 'Account erased', 'Session', '1', '9', '—'],
      [formatDate('2026-11-21T12:00:11Z', 'PPpp'), 'Account activated', 'API key', '1', '9', '—'],
      [formatDate('2026-11-21T12:00:10Z', 'PPpp'), 'Account deactivated', 'SCIM', '—', '9', '—'],
      [
        formatDate('2026-11-21T12:00:09Z', 'PPpp'),
        'Email changed',
        'Single sign-on',
        '1',
        '9',
        '—',
      ],
      [
        formatDate('2026-11-21T12:00:08Z', 'PPpp'),
        'User role changed',
        'Migration',
        '—',
        '9',
        'sales',
      ],
      [
        formatDate('2026-11-21T12:00:07Z', 'PPpp'),
        'Role updated',
        'Session',
        '1',
        '—',
        'Sales lead',
      ],
      [
        formatDate('2026-11-21T12:00:06Z', 'PPpp'),
        'Role deleted',
        'Session',
        '1',
        '—',
        'Sales lead',
      ],
      [
        formatDate('2026-11-21T12:00:05Z', 'PPpp'),
        'Role created',
        'Session',
        '1',
        '—',
        'Sales lead',
      ],
      // An unknown value is shown as stored.
      [
        formatDate('2026-11-21T12:00:04Z', 'PPpp'),
        'some_future_action',
        'future_method',
        '1',
        '—',
        'Sales lead',
      ],
    ]);
    expect(screen.getByText(/of 45/)).toBeInTheDocument();
    expect(lastParams()).toEqual({ offset: 0, limit: 20 });
  });

  it('pages through offset and limit', async () => {
    render(<RoleAudit />);
    await screen.findByText('Account erased');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(lastParams()).toEqual({ offset: 20, limit: 20 }));
    await screen.findByText('Account erased');

    fireEvent.mouseDown(screen.getByRole('combobox', { name: /rows per page/i }));
    fireEvent.click(await screen.findByRole('option', { name: '50' }));
    await waitFor(() => expect(lastParams()).toEqual({ offset: 0, limit: 50 }));
  });

  it('shows the empty state for an empty log', async () => {
    vi.mocked(roleAuditApi.list).mockResolvedValue({ rows: [], total: 0 });
    render(<RoleAudit />);
    expect(await screen.findByText('No role audit rows yet.')).toBeInTheDocument();
  });

  it('shows the API error', async () => {
    vi.mocked(roleAuditApi.list).mockRejectedValue({
      response: { status: 403, data: { message: 'Insufficient permissions' } },
    });
    render(<RoleAudit />);
    expect(await screen.findByText('Insufficient permissions')).toBeInTheDocument();
  });

  it('keeps the current rows on screen while the next page loads', async () => {
    const nextPage = [entry({ id: 2, role_name: 'Next page role' })];
    let release: (value: { rows: RoleAuditEntry[]; total: number }) => void = () => {};
    vi.mocked(roleAuditApi.list).mockImplementation((params) =>
      params.offset === 20
        ? new Promise((resolve) => {
            release = resolve;
          })
        : Promise.resolve({ rows, total: 45 }),
    );
    render(<RoleAudit />);
    await screen.findByText('Account erased');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(lastParams()).toEqual({ offset: 20, limit: 20 }));
    expect(screen.getAllByTestId('role-audit-row')).toHaveLength(rows.length);
    expect(screen.getByText('Account erased')).toBeInTheDocument();
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument();

    release({ rows: nextPage, total: 45 });
    expect(await screen.findByText('Next page role')).toBeInTheDocument();
    expect(screen.getAllByTestId('role-audit-row')).toHaveLength(1);
  });
});
