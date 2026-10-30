import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import {
  ApiClient,
  REFRESH_TOKEN_KEY,
  RefreshTimeoutError,
  SessionChangedError,
  TOKEN_KEY,
} from './client';

// These tests drive the REAL request/response interceptors of ApiClient. The
// transport is a custom axios adapter, so every HTTP failure is rejected as an
// Axios-shaped error explicitly (a custom adapter that resolves a 401 would be
// treated as a success by axios).

type Handler = (config: InternalAxiosRequestConfig) => Promise<AxiosResponse>;

interface Sent {
  url: string;
  method: string;
  authorization?: string;
  body?: Record<string, unknown>;
}

const reply = (config: InternalAxiosRequestConfig, status: number, data: unknown = {}): Promise<AxiosResponse> => {
  const response: AxiosResponse = {
    data,
    status,
    statusText: String(status),
    headers: {},
    config,
  };
  if (status >= 400) {
    return Promise.reject(
      new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', config, null, response)
    );
  }
  return Promise.resolve(response);
};

const ok = (config: InternalAxiosRequestConfig, data: unknown) => reply(config, 200, { success: true, data });
const unauthorized = (config: InternalAxiosRequestConfig) =>
  reply(config, 401, { success: false, error: { code: 'UNAUTHORIZED', message: 'Invalid credentials' } });

/** A promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup(routes: Record<string, Handler>) {
  const sent: Sent[] = [];
  const onSessionExpired = vi.fn();
  const adapter = (config: InternalAxiosRequestConfig) => {
    const url = config.url ?? '';
    const authorization = AxiosHeaders.from(config.headers).get('Authorization') as string | undefined;
    const body = typeof config.data === 'string' && config.data ? JSON.parse(config.data) : undefined;
    sent.push({ url, method: (config.method ?? 'get').toUpperCase(), authorization, body });
    const key = `${(config.method ?? 'get').toUpperCase()} ${url}`;
    const handler = routes[key];
    if (!handler) {
      return reply(config, 404);
    }
    return handler(config);
  };
  const client = new ApiClient({ baseURL: 'http://api.test', adapter, refreshTimeoutMs: 1000, onSessionExpired });
  const api = client.getClient();
  const refreshes = () => sent.filter((s) => s.url === '/auth/refresh');
  return { client, api, sent, refreshes, onSessionExpired };
}

const bearer = (config: InternalAxiosRequestConfig) =>
  AxiosHeaders.from(config.headers).get('Authorization') as string | undefined;

/** A data endpoint that only accepts the given access token. */
const accepts = (token: string, data: unknown = { ok: true }): Handler => (config) =>
  bearer(config) === `Bearer ${token}` ? ok(config, data) : unauthorized(config);

/** An unsigned JWT-shaped token carrying the user id, as the server issues it. */
const jwtFor = (userId: number, tag: string) => {
  const encode = (value: object) => btoa(JSON.stringify(value)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
  return `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode({ user_id: userId, tag })}.signature`;
};

const seedSession = (access: string, refresh: string, persist = false) => {
  const store = persist ? localStorage : sessionStorage;
  store.setItem(TOKEN_KEY, access);
  store.setItem(REFRESH_TOKEN_KEY, refresh);
};

const flush = async () => {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
};

describe('ApiClient 401 refresh', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('concurrent 401s share one refresh and replay every request with the new token', async () => {
    const refresh = deferred<AxiosResponse>();
    const { api, sent, refreshes } = setup({
      'GET /items': accepts('new'),
      'GET /other': accepts('new'),
      'POST /items': (config) =>
        bearer(config) === 'Bearer new' ? ok(config, JSON.parse(config.data as string)) : unauthorized(config),
      'POST /auth/refresh': () => refresh.promise,
    });
    seedSession('old', 'r0');

    const requests = Promise.all([
      api.get('/items'),
      api.get('/other'),
      api.post('/items', { name: 'mutating' }),
    ]);
    await flush();
    refresh.resolve({ data: { success: true, data: { token: 'new', refresh_token: 'r1' } }, status: 200 } as AxiosResponse);

    const [a, b, c] = await requests;
    expect(a.data).toEqual({ ok: true });
    expect(b.data).toEqual({ ok: true });
    expect(c.data).toEqual({ name: 'mutating' });
    expect(refreshes()).toHaveLength(1);
    expect(refreshes()[0].body).toEqual({ refresh_token: 'r0' });
    const replays = sent.filter((s) => s.url !== '/auth/refresh' && s.authorization === 'Bearer new');
    expect(replays).toHaveLength(3);
    expect(sent.filter((s) => s.method === 'POST' && s.url === '/items').map((s) => s.body)).toEqual([
      { name: 'mutating' },
      { name: 'mutating' },
    ]);
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('new');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('r1');
  });

  it('a failed refresh rejects every waiting request and signs out once', async () => {
    const refresh = deferred<AxiosResponse>();
    const { api, refreshes, onSessionExpired } = setup({
      'GET /items': accepts('new'),
      'GET /other': accepts('new'),
      'POST /items': accepts('new'),
      'POST /auth/refresh': (config) => refresh.promise.then(() => unauthorized(config)),
    });
    seedSession('old', 'r0');

    const settled = Promise.allSettled([api.get('/items'), api.get('/other'), api.post('/items', {})]);
    await flush();
    refresh.resolve({} as AxiosResponse);

    const results = await settled;
    // Every waiter is rejected with the refresh's own 401, not by anything else.
    for (const r of results) {
      expect(r.status).toBe('rejected');
      const reason = (r as PromiseRejectedResult).reason as AxiosError;
      expect(reason.response?.status).toBe(401);
      expect(reason.config?.url).toBe('/auth/refresh');
    }
    expect(refreshes()).toHaveLength(1);
    expect(onSessionExpired).toHaveBeenCalledTimes(1);
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
  });

  it('a missing refresh token rejects every waiting request without calling /auth/refresh', async () => {
    const { api, refreshes, onSessionExpired } = setup({
      'GET /items': accepts('new'),
      'GET /other': accepts('new'),
    });
    sessionStorage.setItem(TOKEN_KEY, 'old');

    const results = await Promise.allSettled([api.get('/items'), api.get('/other')]);

    expect(results.every((r) => r.status === 'rejected')).toBe(true);
    expect(refreshes()).toHaveLength(0);
    expect(onSessionExpired).toHaveBeenCalledTimes(1);
  });

  it('a 401 on a replayed request is rejected without a second refresh', async () => {
    const { api, refreshes } = setup({
      'GET /items': (config) => unauthorized(config),
      'GET /other': (config) => unauthorized(config),
      'POST /auth/refresh': (config) => ok(config, { token: 'new', refresh_token: 'r1' }),
    });
    seedSession('old', 'r0');

    const results = await Promise.allSettled([api.get('/items'), api.get('/other')]);

    expect(results.map((r) => r.status)).toEqual(['rejected', 'rejected']);
    for (const r of results) {
      expect(((r as PromiseRejectedResult).reason as AxiosError).response?.status).toBe(401);
    }
    expect(refreshes()).toHaveLength(1);
  });

  it('a refresh that never answers settles every waiter after the timeout', async () => {
    vi.useFakeTimers();
    let answer = false;
    const { api, refreshes } = setup({
      'GET /items': accepts('new'),
      // Ignores the abort signal entirely, the first time.
      'POST /auth/refresh': (config) =>
        answer ? ok(config, { token: 'new', refresh_token: 'r1' }) : new Promise<AxiosResponse>(() => {}),
    });
    seedSession('old', 'r0');

    const settled = Promise.allSettled([api.get('/items'), api.get('/items')]);
    await vi.advanceTimersByTimeAsync(1000);
    const results = await settled;

    expect(results.map((r) => r.status)).toEqual(['rejected', 'rejected']);
    for (const r of results) {
      expect((r as PromiseRejectedResult).reason).toBeInstanceOf(RefreshTimeoutError);
    }
    // A timeout is not a rejection of the session: the tokens stay.
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('r0');

    // The lock was released: a later attempt renews normally.
    answer = true;
    const later = await api.get('/items');
    expect(later.data).toEqual({ ok: true });
    expect(refreshes()).toHaveLength(2);
  });

  it('a 503 from refresh keeps the stored tokens and a later retry renews', async () => {
    let unavailable = true;
    const { api, onSessionExpired } = setup({
      'GET /items': accepts('new'),
      'POST /auth/refresh': (config) =>
        unavailable
          ? reply(config, 503, { success: false, error: { code: 'SESSION_REFRESH_UNAVAILABLE', message: 'retry' } })
          : ok(config, { token: 'new', refresh_token: 'r1' }),
    });
    seedSession('old', 'r0');

    await expect(api.get('/items')).rejects.toMatchObject({ response: { status: 503 } });
    expect(onSessionExpired).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('old');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('r0');

    unavailable = false;
    await expect(api.get('/items')).resolves.toMatchObject({ data: { ok: true } });
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('r1');
  });

  it('logout during an in-flight refresh discards the late response and rejects the waiters', async () => {
    const refresh = deferred<AxiosResponse>();
    const { client, api, sent, onSessionExpired } = setup({
      'GET /items': accepts('new'),
      'POST /auth/refresh': () => refresh.promise,
    });
    seedSession('old', 'r0', true);
    const revoke = vi.fn(async () => {});

    const pending = api.get('/items');
    await flush();
    const ending = client.endSession(revoke);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(revoke).not.toHaveBeenCalled();

    refresh.resolve({ data: { success: true, data: { token: 'new', refresh_token: 'r1' } }, status: 200 } as AxiosResponse);
    await expect(pending).rejects.toBeInstanceOf(SessionChangedError);
    await ending;

    // The refresh spent r0 and committed a successor; the revocation is
    // authenticated with that successor, which is never stored.
    expect(revoke).toHaveBeenCalledTimes(1);
    expect(revoke).toHaveBeenCalledWith('new');
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    expect(sent.filter((s) => s.authorization === 'Bearer new')).toHaveLength(0);
    expect(onSessionExpired).not.toHaveBeenCalled();
  });

  it('logout with an expired access token renews privately so the revocation is authenticated', async () => {
    const { client } = setup({
      'POST /auth/refresh': (config) => ok(config, { token: 'renewed', refresh_token: 'r1' }),
    });
    seedSession('expired', 'r0');
    const revoke = vi.fn(async (token: string) => {
      if (token === 'expired') {
        throw new AxiosError('401', 'ERR_BAD_REQUEST', undefined, null, { status: 401 } as AxiosResponse);
      }
    });

    await client.endSession(revoke);

    expect(revoke.mock.calls.map((c) => c[0])).toEqual(['expired', 'renewed']);
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
  });

  it('logout with an expired access token during an in-flight refresh revokes with the successor', async () => {
    // The in-flight refresh consumes r0 and returns the successor. Logout must
    // authenticate its revocation with that successor (privately: it is never
    // stored), because r0 is spent and the old access token has expired.
    const refresh = deferred<AxiosResponse>();
    let refreshCalls = 0;
    const { client, api } = setup({
      'GET /items': accepts('successor-access'),
      'POST /auth/refresh': (config) => {
        refreshCalls += 1;
        return refreshCalls === 1 ? refresh.promise : unauthorized(config);
      },
    });
    seedSession('expired', 'r0', true);
    const revoke = vi.fn(async (token: string) => {
      if (token !== 'successor-access') {
        throw new AxiosError('401', 'ERR_BAD_REQUEST', undefined, null, { status: 401 } as AxiosResponse);
      }
    });

    const pending = api.get('/items').catch((e) => e);
    await flush();
    const ending = client.endSession(revoke);
    refresh.resolve({
      data: { success: true, data: { token: 'successor-access', refresh_token: 'r1' } },
      status: 200,
    } as AxiosResponse);

    await expect(ending).resolves.toBeUndefined();
    expect(await pending).toBeInstanceOf(SessionChangedError);
    expect(revoke).toHaveBeenCalledWith('successor-access');
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it('signing in to another account during an in-flight refresh keeps the new session', async () => {
    const refresh = deferred<AxiosResponse>();
    const { client, api, sent } = setup({
      'GET /items': accepts('a-new'),
      'POST /auth/refresh': () => refresh.promise,
    });
    seedSession('a-old', 'a-r0');

    const pending = api.get('/items');
    await flush();
    client.startSession('b-token', 'b-refresh', true);
    refresh.resolve({ data: { success: true, data: { token: 'a-new', refresh_token: 'a-r1' } }, status: 200 } as AxiosResponse);

    await expect(pending).rejects.toBeInstanceOf(SessionChangedError);
    expect(localStorage.getItem(TOKEN_KEY)).toBe('b-token');
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('b-refresh');
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(sent.filter((s) => s.url === '/items' && s.authorization === 'Bearer b-token')).toHaveLength(0);
  });

  it('a request sent under an ended session is never replayed under the next one', async () => {
    const late401 = deferred<void>();
    const { client, api, sent, refreshes } = setup({
      'POST /items': (config) => late401.promise.then(() => unauthorized(config)),
    });
    seedSession('a-token', 'a-refresh');

    const pending = api.post('/items', { name: 'sent as A' });
    await flush();
    client.startSession('b-token', 'b-refresh', false);
    late401.resolve();

    await expect(pending).rejects.toBeInstanceOf(SessionChangedError);
    expect(refreshes()).toHaveLength(0);
    expect(sent.filter((s) => s.url === '/items')).toHaveLength(1);
  });

  it('a refresh answered after the password change stored new tokens does not overwrite them', async () => {
    vi.useFakeTimers();
    const stale = deferred<AxiosResponse>();
    let firstRefresh = true;
    const { client, api } = setup({
      'GET /items': accepts('pc-token'),
      'POST /auth/refresh': () => {
        if (firstRefresh) {
          firstRefresh = false;
          return stale.promise;
        }
        return new Promise<AxiosResponse>(() => {});
      },
      'POST /auth/change-password': (config) =>
        ok(config, { message: 'Password changed successfully', token: 'pc-token', refresh_token: 'pc-refresh' }),
    });
    seedSession('old', 'r0');

    const pending = api.get('/items').catch((e) => e);
    await flush();
    const change = client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    // The refresh times out; the password change then runs and stores its pair.
    await vi.advanceTimersByTimeAsync(1000);
    await expect(change).resolves.toEqual({ renewed: true });
    expect(await pending).toBeInstanceOf(RefreshTimeoutError);

    // The abandoned refresh answers late. It must not overwrite anything.
    stale.resolve({ data: { success: true, data: { token: 'late', refresh_token: 'late-r' } }, status: 200 } as AxiosResponse);
    await flush();
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('pc-token');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('pc-refresh');
  });
});

describe('ApiClient.changePassword', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  const renewing = (config: InternalAxiosRequestConfig) =>
    ok(config, { message: 'Password changed successfully', token: 'pc-token', refresh_token: 'pc-refresh' });

  it('stores the replacement tokens in sessionStorage when the session was not remembered', async () => {
    const { client, sent } = setup({ 'POST /auth/change-password': renewing });
    seedSession('old', 'r0', false);

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).resolves.toEqual({ renewed: true });

    expect(sent[0].body).toEqual({
      current_password: 'OldStrongPass1!',
      new_password: 'NewStrongPass1!',
      refresh_token: 'r0',
    });
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('pc-token');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('pc-refresh');
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
  });

  it('stores the replacement tokens in localStorage when the session was remembered', async () => {
    const { client } = setup({ 'POST /auth/change-password': renewing });
    seedSession('old', 'r0', true);

    await client.changePassword('OldStrongPass1!', 'NewStrongPass1!');

    expect(localStorage.getItem(TOKEN_KEY)).toBe('pc-token');
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('pc-refresh');
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it('waits for an in-flight refresh and presents the rotated refresh token', async () => {
    const refresh = deferred<AxiosResponse>();
    const { client, api, sent } = setup({
      'GET /items': accepts('new'),
      'POST /auth/refresh': () => refresh.promise,
      'POST /auth/change-password': renewing,
    });
    seedSession('old', 'r0');

    const pending = api.get('/items');
    await flush();
    const change = client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await flush();
    expect(sent.filter((s) => s.url === '/auth/change-password')).toHaveLength(0);

    refresh.resolve({ data: { success: true, data: { token: 'new', refresh_token: 'r1' } }, status: 200 } as AxiosResponse);
    await pending;
    await change;

    const body = sent.find((s) => s.url === '/auth/change-password')?.body;
    expect(body?.refresh_token).toBe('r1');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('pc-refresh');
  });

  it('keeps the current tokens when the server returns no token pair', async () => {
    const { client } = setup({
      'POST /auth/change-password': (config) => ok(config, { message: 'Password changed successfully' }),
    });
    seedSession('old', 'r0');

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).resolves.toEqual({ renewed: false });
    expect(sessionStorage.getItem(TOKEN_KEY)).toBe('old');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('r0');
  });

  it('refreshes once and retries when the access token expired', async () => {
    // Three requests interleave: the password change holds the session lock,
    // a data request hits 401 and queues a refresh behind it, and the change
    // itself hits 401. The change must renew privately (not wait on the
    // queued refresh), rebuild its body from the renewed pair, and the queued
    // request must then replay with the token the change stored.
    const oldAccess = jwtFor(1, 'old');
    const newAccess = jwtFor(1, 'new');
    const firstChange = deferred<void>();
    const { client, api, sent, refreshes } = setup({
      'GET /items': accepts('pc-token'),
      'POST /auth/refresh': (config) => ok(config, { token: newAccess, refresh_token: 'r1' }),
      'POST /auth/change-password': (config) => {
        if (bearer(config) === `Bearer ${oldAccess}`) {
          return firstChange.promise.then(() => unauthorized(config));
        }
        return renewing(config);
      },
    });
    seedSession(oldAccess, 'r0');

    const change = client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await flush();
    const data = api.get('/items');
    await flush();
    firstChange.resolve();

    await expect(change).resolves.toEqual({ renewed: true });
    await expect(data).resolves.toMatchObject({ data: { ok: true } });

    const posts = sent.filter((s) => s.url === '/auth/change-password');
    expect(posts).toHaveLength(2);
    expect(posts[1].authorization).toBe(`Bearer ${newAccess}`);
    expect(posts[1].body).toEqual({
      current_password: 'OldStrongPass1!',
      new_password: 'NewStrongPass1!',
      refresh_token: 'r1',
    });
    expect(refreshes()).toHaveLength(1);
    expect(sent.filter((s) => s.url === '/items').map((s) => s.authorization)).toEqual([
      `Bearer ${oldAccess}`,
      'Bearer pc-token',
    ]);
  });

  it('retries once with the replacement token when another tab renewed the session', async () => {
    const before = jwtFor(1, 'before');
    const renewed = jwtFor(1, 'renewed-by-other-tab');
    let calls = 0;
    const { client, sent } = setup({
      'POST /auth/change-password': (config) => {
        calls += 1;
        if (calls === 1) {
          // Another tab of this remembered session (same account) rotated it.
          localStorage.setItem(TOKEN_KEY, renewed);
          localStorage.setItem(REFRESH_TOKEN_KEY, 'r-other-tab');
          return reply(config, 409, { success: false, error: { code: 'SESSION_STALE', message: 'stale' } });
        }
        return renewing(config);
      },
    });
    seedSession(before, 'r0', true);

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).resolves.toEqual({ renewed: true });
    expect(sent.map((s) => s.body?.refresh_token)).toEqual(['r0', 'r-other-tab']);
    expect(sent.map((s) => s.authorization)).toEqual([`Bearer ${before}`, `Bearer ${renewed}`]);
  });

  it('does not retry a stale password change under another account signed in by another tab', async () => {
    const accountA = jwtFor(1, 'a');
    const accountB = jwtFor(2, 'b');
    const { client, sent } = setup({
      'POST /auth/change-password': (config) => {
        // Another tab logged A out and signed B in, in the shared storage.
        localStorage.setItem(TOKEN_KEY, accountB);
        localStorage.setItem(REFRESH_TOKEN_KEY, 'b-refresh');
        return reply(config, 409, { success: false, error: { code: 'SESSION_STALE', message: 'stale' } });
      },
    });
    seedSession(accountA, 'a-refresh', true);

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).rejects.toMatchObject({
      response: { status: 409 },
    });
    expect(sent).toHaveLength(1);
    expect(sent.filter((s) => s.authorization === `Bearer ${accountB}`)).toHaveLength(0);
    expect(localStorage.getItem(TOKEN_KEY)).toBe(accountB);
  });

  it('does not renew or retry under another account when the access token expired', async () => {
    const accountA = jwtFor(1, 'a-expired');
    const accountB = jwtFor(2, 'b');
    const { client, sent, refreshes } = setup({
      'POST /auth/refresh': (config) => ok(config, { token: jwtFor(2, 'b-new'), refresh_token: 'b-r1' }),
      'POST /auth/change-password': (config) => {
        localStorage.setItem(TOKEN_KEY, accountB);
        localStorage.setItem(REFRESH_TOKEN_KEY, 'b-refresh');
        return unauthorized(config);
      },
    });
    seedSession(accountA, 'a-refresh', true);

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).rejects.toMatchObject({
      response: { status: 401 },
    });
    expect(refreshes()).toHaveLength(0);
    expect(sent.filter((s) => s.url === '/auth/change-password')).toHaveLength(1);
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('b-refresh');
  });

  it('a stale session without a newer token is reported, not retried', async () => {
    const { client, sent } = setup({
      'POST /auth/change-password': (config) =>
        reply(config, 409, { success: false, error: { code: 'SESSION_STALE', message: 'stale' } }),
    });
    seedSession('old', 'r0');

    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).rejects.toMatchObject({
      response: { status: 409, data: { code: 'SESSION_STALE' } },
    });
    expect(sent).toHaveLength(1);
  });

  it('a password change answered after sign-out stores nothing', async () => {
    const answer = deferred<void>();
    const { client } = setup({
      'POST /auth/change-password': (config) => answer.promise.then(() => renewing(config)),
    });
    seedSession('old', 'r0');

    const change = client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await flush();
    client.clearTokens();
    answer.resolve();

    await expect(change).rejects.toBeInstanceOf(SessionChangedError);
    expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
  });
});

describe('ApiClient session storage', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it('startSession removes a predecessor refresh token when the response omits one', () => {
    const { client } = setup({});
    seedSession('a-token', 'a-refresh', true);

    client.startSession('b-token', undefined, true);

    expect(client.getToken()).toBe('b-token');
    expect(client.getRefreshToken()).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
  });

  it('never pairs an access token with a refresh token from the other storage', () => {
    const { client } = setup({});
    // A JWT-only session in this tab, and a remembered session another tab wrote.
    sessionStorage.setItem(TOKEN_KEY, 'tab-token');
    localStorage.setItem(TOKEN_KEY, 'remembered-token');
    localStorage.setItem(REFRESH_TOKEN_KEY, 'remembered-refresh');

    expect(client.getToken()).toBe('tab-token');
    expect(client.getRefreshToken()).toBeNull();
  });
});
