import { describe, expect, it } from 'vitest';
import { apiErrorMessage, getAPIError, getErrorMessage, getHTTPStatus } from './apiError';

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

describe('apiErrorMessage', () => {
  const apiFailure = (status: number, data: unknown) => ({ response: { status, data } });

  it('returns the flat server message', () => {
    expect(apiErrorMessage(apiFailure(500, { message: 'Internal server error' }), 'Failed')).toBe(
      'Internal server error',
    );
  });

  it.each([{ message: '' }, { message: {} }, {}])(
    'falls back when the message is empty, not a string or missing: %j',
    (data) => {
      expect(apiErrorMessage(apiFailure(500, data), 'Failed')).toBe('Failed');
    },
  );

  it.each([null, new Error('Network Error'), 'network failure'])(
    'falls back for a rejection without an API error body: %s',
    (error) => {
      expect(apiErrorMessage(error, 'Failed')).toBe('Failed');
    },
  );

  it('appends the field detail of a 400 answer when asked', () => {
    const error = apiFailure(400, {
      message: 'Validation failed',
      details: { name: 'must be 1 to 100 characters after trimming' },
    });
    expect(apiErrorMessage(error, 'Failed', { details: true })).toBe(
      'Validation failed: name must be 1 to 100 characters after trimming',
    );
  });

  it('joins several field details in insertion order', () => {
    const error = apiFailure(400, {
      message: 'Validation failed',
      details: { name: 'is required', email: 'must be an email address' },
    });
    expect(apiErrorMessage(error, 'Failed', { details: true })).toBe(
      'Validation failed: name is required; email must be an email address',
    );
  });

  it('names a field whose detail is not a string as invalid', () => {
    const error = apiFailure(400, { message: 'Validation failed', details: { count: 3 } });
    expect(apiErrorMessage(error, 'Failed', { details: true })).toBe(
      'Validation failed: count is invalid',
    );
  });

  it('returns the message alone for a 400 without the details option', () => {
    const error = apiFailure(400, {
      message: 'Validation failed',
      details: { name: 'is required' },
    });
    expect(apiErrorMessage(error, 'Failed')).toBe('Validation failed');
  });

  it('returns the message alone for a 429 even with the details option', () => {
    const error = apiFailure(429, {
      message: 'Too many requests. Please try again later.',
      details: { retry_after: '60s' },
    });
    expect(apiErrorMessage(error, 'Failed', { details: true })).toBe(
      'Too many requests. Please try again later.',
    );
  });

  it('returns the message alone for a 409 even with the details option', () => {
    const error = apiFailure(409, {
      message: 'A company with this domain already exists',
      details: { domain: 'already exists' },
    });
    expect(apiErrorMessage(error, 'Failed', { details: true })).toBe(
      'A company with this domain already exists',
    );
  });
});
