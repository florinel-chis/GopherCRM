import { execFileSync } from 'child_process';
import path from 'path';
import { testAdminCredentials } from './fixtures/admin-user';

const repoRoot = path.resolve(__dirname, '..', '..');

/**
 * Provisions the admin account the admin E2E suites log in as.
 *
 * The public /auth/register endpoint always creates a customer, so the admin has
 * to be seeded out-of-band through the same CLI an operator would use. Re-running
 * this is harmless: create-admin exits non-zero when the account already exists.
 */
export default function globalSetup() {
  try {
    execFileSync(
      'go',
      [
        'run',
        './cmd/create-admin',
        '-non-interactive',
        '-email',
        testAdminCredentials.email,
        '-password',
        testAdminCredentials.password,
        '-name',
        'Test Admin',
      ],
      { cwd: repoRoot, stdio: 'pipe' }
    );
    console.log(`[global-setup] provisioned admin ${testAdminCredentials.email}`);
  } catch (error) {
    const output = [
      (error as { stdout?: Buffer }).stdout?.toString() ?? '',
      (error as { stderr?: Buffer }).stderr?.toString() ?? '',
    ].join('');

    if (/already exists/i.test(output)) {
      console.log(`[global-setup] admin ${testAdminCredentials.email} already exists`);
      return;
    }
    throw new Error(`[global-setup] failed to provision admin user:\n${output}`);
  }
}
