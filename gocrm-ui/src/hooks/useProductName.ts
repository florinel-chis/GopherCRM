import { useContext } from 'react';
import type { Configuration } from '@/api/endpoints/configurations';
import { ConfigurationContext } from '@/contexts/configuration-context';

/** The last-resort product name when nothing is stored or built in. */
export const FALLBACK_PRODUCT_NAME = 'GoCRM';

const COMPANY_NAME_KEY = 'general.company_name';

const nonBlank = (value: unknown): value is string =>
  typeof value === 'string' && value.trim() !== '';

/** The build-time name: VITE_APP_NAME, else 'GoCRM'. */
export const buildTimeProductName = (): string => {
  const value: unknown = import.meta.env.VITE_APP_NAME;
  return nonBlank(value) ? value : FALLBACK_PRODUCT_NAME;
};

// Parses an entry the way configurationsApi.getValue does, keeping only the
// outcomes that can be a string: a 'string' entry as is, a 'json' entry parsed.
const storedString = (config: Configuration): unknown => {
  if (config.type === 'string') return config.value;
  if (config.type === 'json') {
    try {
      return JSON.parse(config.value);
    } catch {
      return undefined;
    }
  }
  return undefined;
};

/**
 * The product name the web app shows: the stored `general.company_name`
 * exactly as stored when the signed-in user's configuration holds a non-blank
 * string for it, otherwise VITE_APP_NAME, otherwise 'GoCRM'.
 *
 * It reads the context directly (never getCompanyName(), whose fallback is a
 * different name, nor useConfiguration(), which throws outside a provider).
 * Signed out the provider serves no configuration, so the sign-in and
 * registration pages show the build-time name from their first render and
 * make no configuration request.
 */
export const useProductName = (): string => {
  const configuration = useContext(ConfigurationContext);
  const entry = configuration?.configurations.find((config) => config.key === COMPANY_NAME_KEY);
  const stored = entry ? storedString(entry) : undefined;
  return nonBlank(stored) ? stored : buildTimeProductName();
};
