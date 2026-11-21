import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { roleAuditApi, ROLE_AUDIT_ACTIONS, ROLE_AUDIT_METHODS } from './roleAudit';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
  },
}));

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..');
const swagger = JSON.parse(readFileSync(path.join(repoRoot, 'api', 'swagger.json'), 'utf8')) as {
  definitions: Record<string, { properties: Record<string, { enum?: unknown[] }> }>;
};

const documentedEnum = (property: string): string[] =>
  [
    ...(swagger.definitions['internal_handler.RoleAuditEntryResponse'].properties[property].enum ??
      []),
  ]
    .map(String)
    .sort();

const entry = {
  id: 7,
  created_at: '2026-11-21T09:30:00Z',
  actor_user_id: 1,
  auth_method: 'bearer',
  action: 'user_deactivated',
  role_id: null,
  role_name: '',
  target_user_id: 12,
  before_state: { is_active: true },
  after_state: { is_active: false },
};

describe('role audit lists against the API specification', () => {
  it('ROLE_AUDIT_ACTIONS equals the action enum of RoleAuditEntryResponse', () => {
    expect(ROLE_AUDIT_ACTIONS).toHaveLength(8);
    expect([...ROLE_AUDIT_ACTIONS].sort()).toEqual(documentedEnum('action'));
  });

  it('ROLE_AUDIT_METHODS equals the auth_method enum of RoleAuditEntryResponse', () => {
    expect([...ROLE_AUDIT_METHODS].sort()).toEqual(documentedEnum('auth_method'));
  });
});

describe('roleAuditApi.list', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('sends offset and limit only and returns the rows with the total', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [entry], meta: { total: 41 } });

    const result = await roleAuditApi.list({ offset: 40, limit: 20 });

    expect(api.get).toHaveBeenCalledWith('/roles/audit', { params: { offset: 40, limit: 20 } });
    expect(result).toEqual({ rows: [entry], total: 41 });
  });

  it('answers an empty list when the data is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: null });
    expect(await roleAuditApi.list({ offset: 0, limit: 20 })).toEqual({ rows: [], total: 0 });
  });
});
