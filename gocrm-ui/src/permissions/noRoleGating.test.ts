import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// The SPA decides by the permission keys of GET /users/me, never by the name
// of the signed-in user's role. This test reads every source file and fails
// on the forms a role gate took before the conversion.

const srcRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const ROLE_GATE_PATTERNS: RegExp[] = [
  /requiredRole/,
  /hasRole/,
  /roles: \[/,
  /(user|currentUser)\?\.role/,
  /\.role (===|!==)/,
  /includes\([^)]*\.role\b/,
  /\brole (===|!==)/,
];

const isRoleGate = (line: string): boolean => ROLE_GATE_PATTERNS.some((pattern) => pattern.test(line));

// The two places that read the role of ANOTHER user (who may be picked as an
// assignee or a form owner), not of the signed-in one.
const ALLOWED: Array<{ file: string; line: RegExp }> = [
  { file: 'pages/automation/StaffUserAutocomplete.tsx', line: /^const isAssignable = / },
  { file: 'pages/forms/SubmissionActionSection.tsx', line: /candidate\.is_active && \(candidate\.role === / },
];

const sourceFiles = (dir: string): string[] =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      // src/test holds test support code only.
      return full === path.join(srcRoot, 'test') ? [] : sourceFiles(full);
    }
    if (!/\.tsx?$/.test(entry.name) || /\.test\.tsx?$/.test(entry.name)) return [];
    return [full];
  });

describe('permission gating', () => {
  it('no page gates on the role of the signed-in user', () => {
    const files = sourceFiles(srcRoot);
    // The walk really covered the tree.
    expect(files.length).toBeGreaterThan(100);
    expect(files).toContain(path.join(srcRoot, 'components', 'ProtectedRoute.tsx'));

    const hits: string[] = [];
    const allowedSeen = new Set<number>();
    for (const file of files) {
      const rel = path.relative(srcRoot, file).split(path.sep).join('/');
      readFileSync(file, 'utf8')
        .split('\n')
        .forEach((line, index) => {
          if (!isRoleGate(line)) return;
          const allowed = ALLOWED.findIndex((entry) => entry.file === rel && entry.line.test(line.trim()));
          if (allowed >= 0) {
            allowedSeen.add(allowed);
            return;
          }
          hits.push(`${rel}:${index + 1}: ${line.trim()}`);
        });
    }

    expect(hits).toEqual([]);
    // An allowlist entry that no longer matches anything is stale.
    expect(allowedSeen.size).toBe(ALLOWED.length);
  });

  it('the detector flags every form a role gate took', () => {
    const samples = [
      "user?.role === 'admin'",
      'requiredRole="admin"',
      "roles: ['admin']",
      'item.roles.includes(user.role)',
      "role === 'admin' || role === 'sales'",
      "if (role !== 'admin') return null",
    ];
    for (const sample of samples) {
      expect(isRoleGate(sample), sample).toBe(true);
    }
    // Permission checks and role labels are not gates.
    expect(isRoleGate("const canCreate = can('tickets.create');")).toBe(false);
    expect(isRoleGate('label={user.role}')).toBe(false);
  });
});
