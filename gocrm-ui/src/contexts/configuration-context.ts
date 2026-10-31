import { createContext } from 'react';
import type { Configuration, LeadConversionSettings } from '@/api/endpoints/configurations';

/**
 * What the UI knows about the lead-conversion rules. They are policy, so
 * nothing is invented while they are unknown: `loading` while the session or
 * the configuration loads, `unavailable` when the load failed or the server
 * left out the allowed statuses or the notes rule (it omits an entry it
 * could not read). Only `ready` carries settings.
 */
export type LeadConversionState =
  | { status: 'loading' }
  | { status: 'unavailable' }
  | { status: 'ready'; settings: LeadConversionSettings };

export interface ConfigurationContextType {
  /** The UI-safe configuration of the signed-in user; empty without a session. */
  configurations: Configuration[];
  /** True while the session is being restored or its configuration loads. */
  isLoading: boolean;
  /** Why the last load failed. */
  error: string | null;
  /** The lead-conversion rules, or why they are not known. */
  getLeadConversion: () => LeadConversionState;
  /** Display value with a built-in fallback ('GopherCRM'). */
  getCompanyName: () => string;
  /** Display value with a built-in fallback ('#1976d2'). */
  getPrimaryColor: () => string;
  /** Re-reads the configuration (after an admin saved a setting, or to retry a failed load). */
  refreshConfigurations: () => Promise<void>;
}

/**
 * Every cached copy of GET /configurations/ui starts with this key, so one
 * invalidation refreshes all of them.
 */
export const UI_CONFIGURATIONS_KEY = ['configurations', 'ui'] as const;

export const ConfigurationContext = createContext<ConfigurationContextType | undefined>(undefined);
