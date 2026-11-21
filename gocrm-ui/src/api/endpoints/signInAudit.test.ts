import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import {
  signInAuditApi,
  SIGN_IN_AUDIT_METHODS,
  SIGN_IN_AUDIT_OUTCOMES,
  SIGN_IN_AUDIT_REASONS,
} from './signInAudit';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
  },
}));

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..');
const swagger = JSON.parse(readFileSync(path.join(repoRoot, 'api', 'swagger.json'), 'utf8')) as {
  paths: Record<
    string,
    Record<string, { parameters: { name: string; in: string; enum?: unknown[] }[] }>
  >;
};

const documentedEnum = (name: string): string[] => {
  const parameter = swagger.paths['/audit/sign-ins'].get.parameters.find(
    (p) => p.in === 'query' && p.name === name,
  );
  return [...(parameter?.enum ?? [])].map(String).sort();
};

const entry = {
  id: 12,
  created_at: '2026-11-20T09:30:00Z',
  method: 'password',
  outcome: 'success',
  reason: '',
  user_id: 1,
  attempt_id: null,
  request_id: 'req-1',
  subject_ref: null,
  claim_roles: null,
  claim_groups: null,
  client_network: '192.0.2.0/24',
  user_agent_family: 'Chrome on Linux',
  attempts: 1,
};

describe('sign-in audit lists against the API specification', () => {
  it('SIGN_IN_AUDIT_METHODS equals the method enum of GET /audit/sign-ins', () => {
    expect([...SIGN_IN_AUDIT_METHODS].sort()).toEqual(documentedEnum('method'));
  });

  it('SIGN_IN_AUDIT_OUTCOMES equals the outcome enum of GET /audit/sign-ins', () => {
    expect([...SIGN_IN_AUDIT_OUTCOMES].sort()).toEqual(documentedEnum('outcome'));
  });

  it('SIGN_IN_AUDIT_REASONS equals the reason enum of GET /audit/sign-ins', () => {
    expect(SIGN_IN_AUDIT_REASONS).toHaveLength(43);
    expect([...SIGN_IN_AUDIT_REASONS].sort()).toEqual(documentedEnum('reason'));
  });
});

describe('signInAuditApi.list', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('sends only the filters that are set, with offset and limit', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [entry], meta: { total: 41 } });

    const result = await signInAuditApi.list({
      outcome: 'refused',
      reason: 'no_mapping_match',
      method: undefined,
      user_id: undefined,
      offset: 0,
      limit: 20,
    });

    expect(api.get).toHaveBeenCalledWith('/audit/sign-ins', {
      params: { outcome: 'refused', reason: 'no_mapping_match', offset: 0, limit: 20 },
    });
    expect(result).toEqual({ rows: [entry], total: 41 });
  });

  it('answers an empty list when the data is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: null });
    expect(await signInAuditApi.list({ offset: 0, limit: 20 })).toEqual({ rows: [], total: 0 });
  });
});
