import type { Configuration, ConfigurationValue } from '@/api/endpoints/configurations';

export function isConfigurationValue(value: unknown): value is ConfigurationValue {
  if (value === null || typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return true;
  if (Array.isArray(value)) return value.every(isConfigurationValue);
  return typeof value === 'object' && Object.values(value).every(isConfigurationValue);
}

/** The setting's list of allowed values, or null when it has none or it is not a JSON array. */
export function parseValidValues(config: Configuration): ConfigurationValue[] | null {
  if (!config.valid_values) return null;
  try {
    const parsed: unknown = JSON.parse(config.valid_values);
    return Array.isArray(parsed) && parsed.every(isConfigurationValue) ? parsed : null;
  } catch {
    return null;
  }
}

/**
 * Whether the editor keeps a raw text draft that is parsed on Save: JSON,
 * free-form arrays and numbers. Intermediate keystrokes ("{", "-", "") are not
 * valid values, so the field holds exactly what was typed until then.
 */
export function usesDraft(config: Configuration): boolean {
  if (config.is_sensitive) return false;
  switch (config.type) {
    case 'json':
    case 'integer':
    case 'float':
      return true;
    case 'array':
      return parseValidValues(config) === null;
    default:
      return false;
  }
}

/** The draft an editor starts from for the setting's parsed value. */
export function initialDraft(config: Configuration, value: unknown): string {
  if (config.type === 'integer' || config.type === 'float') {
    return typeof value === 'number' && Number.isFinite(value) ? String(value) : '';
  }
  return JSON.stringify(value, null, 2) ?? '';
}

export type DraftResult = { value: Exclude<ConfigurationValue, null> } | { error: string };

/** Parse a draft into the value to save, or the field error to show. */
export function parseDraft(config: Configuration, draft: string): DraftResult {
  if (config.type === 'integer' || config.type === 'float') {
    const number = draft.trim() === '' ? NaN : Number(draft);
    if (!Number.isFinite(number)) return { error: 'Enter a number' };
    if (config.type === 'integer' && !Number.isInteger(number)) return { error: 'Enter a whole number' };
    return { value: number };
  }

  const invalid = config.type === 'array' ? 'Enter a JSON array' : 'Enter valid JSON';
  let parsed: unknown;
  try {
    parsed = JSON.parse(draft);
  } catch {
    return { error: invalid };
  }
  if (config.type === 'array' && !Array.isArray(parsed)) return { error: invalid };
  // The API stores no null value; a typed `null` is refused like invalid JSON.
  if (parsed === null || !isConfigurationValue(parsed)) return { error: invalid };
  return { value: parsed };
}
