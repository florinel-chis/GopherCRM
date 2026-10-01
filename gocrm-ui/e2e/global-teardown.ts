import { setPublicRegistration } from './helpers/registration-config';

/**
 * Puts security.allow_public_registration back the way global-setup found it.
 *
 * The e2e database is reset per run, so there this is a harmless no-op-shaped
 * restore; the screenshots config, however, runs the same global setup against
 * the dev backend on 8090, and without this a screenshots run would leave that
 * deployment's sign-up permanently reopened.
 *
 * A failed restore only warns: the backend may already be gone by teardown
 * time in aborted runs, and the next global setup re-reads the real state.
 */
export default async function globalTeardown() {
  if (process.env.E2E_REGISTRATION_WAS_OPEN === 'true') {
    return; // it was open before the run; leaving it open IS the restore
  }
  try {
    await setPublicRegistration(false);
    console.log('[global-teardown] public registration restored to disabled');
  } catch (error) {
    console.warn(`[global-teardown] could not restore the registration switch: ${error}`);
  }
}
