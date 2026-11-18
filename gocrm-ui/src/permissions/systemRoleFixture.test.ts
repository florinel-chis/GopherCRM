import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { systemRolePermissions } from './systemRoleFixture';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..');
const golden = path.join(repoRoot, 'internal', 'permissions', 'testdata', 'system_roles.golden.json');

describe('system role fixture', () => {
  it('equals the golden matrix', () => {
    const matrix: unknown = JSON.parse(readFileSync(golden, 'utf8'));

    expect(systemRolePermissions).toEqual(matrix);
    // toEqual ignores key order; the roles and their keys keep the golden's.
    expect(JSON.stringify(systemRolePermissions)).toBe(JSON.stringify(matrix));
  });
});
