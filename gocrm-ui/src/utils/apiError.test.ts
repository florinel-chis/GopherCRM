import { describe, expect, it } from 'vitest';
import { getAPIError, getErrorMessage, getHTTPStatus } from './apiError';

describe('API error readers', () => {
  it('preserves unwrapped server messages and field errors', () => {
    const payload = {
      code: 'INVALID_REFERENCE',
      message: 'Unknown company_id',
      details: { Email: 'Invalid email address' },
    };
    expect(getAPIError({ response: { data: payload } })).toEqual(payload);
  });

  it.each([null, undefined, 'network failure', {}, { response: null }, { response: { data: [] } }])(
    'handles a rejection without an API error payload: %j',
    (error) => {
      expect(getAPIError(error)).toBeUndefined();
      expect(getHTTPStatus(error)).toBeUndefined();
    },
  );

  it('does not expose malformed messages or validation details as typed values', () => {
    expect(
      getAPIError({ response: { data: { code: 400, message: {}, details: ['invalid'] } } }),
    ).toEqual({ code: undefined, message: undefined, details: undefined });
  });

  it('reads numeric authentication statuses without requiring an Axios instance', () => {
    expect(getHTTPStatus({ response: { status: 401 } })).toBe(401);
    expect(getHTTPStatus({ response: { status: 403 } })).toBe(403);
    expect(getHTTPStatus({ response: { status: '401' } })).toBeUndefined();
  });

  it('preserves ordinary error messages and falls back for missing messages', () => {
    expect(getErrorMessage(new Error('Network Error'), 'Failed')).toBe('Network Error');
    expect(getErrorMessage({ message: 'Offline' }, 'Failed')).toBe('Offline');
    expect(getErrorMessage({ message: '' }, 'Failed')).toBe('Failed');
    expect(getErrorMessage(null, 'Failed')).toBe('Failed');
  });
});
