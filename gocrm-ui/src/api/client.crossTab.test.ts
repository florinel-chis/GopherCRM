import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import {
  ApiClient,
  REFRESH_TOKEN_KEY,
  RefreshTimeoutError,
  SESSION_KEY,
  SessionChangedError,
  TOKEN_KEY,
  type ApiClientOptions,
} from './client';
import { createMemorySessionLock } from './sessionLock';

// Two ApiClient instances stand for two browser tabs: they share the browser
// storages and, unless a test says otherwise, one memory lock (what the Web
// Lock `gophercrm-session` is across tabs). One fake server answers both, and
// every request is logged with the tab that sent it.

type Route = (config: InternalAxiosRequestConfig, tab: string) => Promise<AxiosResponse>;

interface Sent {
  tab: string;
  url: string;
  authorization?: string;
  body?: Record<string, unknown>;
}

const reply = (config: InternalAxiosRequestConfig, status: number, data: unknown = {}): Promise<AxiosResponse> => {
  const response: AxiosResponse = { data, status, statusText: String(status), headers: {}, config };
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
const stale = (config: InternalAxiosRequestConfig) =>
  reply(config, 409, { success: false, error: { code: 'SESSION_STALE', message: 'stale' } });
const bearer = (config: InternalAxiosRequestConfig) =>
  AxiosHeaders.from(config.headers).get('Authorization') as string | undefined;
const bodyOf = (config: InternalAxiosRequestConfig): Record<string, unknown> =>
  typeof config.data === 'string' && config.data ? (JSON.parse(config.data) as Record<string, unknown>) : {};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const flush = async () => {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
};

const jwtFor = (userId: number, tag: string) => {
  const encode = (value: object) =>
    btoa(JSON.stringify(value)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
  return `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode({ user_id: userId, tag })}.signature`;
};

function createBrowser(routes: Record<string, Route>) {
  const sent: Sent[] = [];
  const tab = (name: string, options: Partial<ApiClientOptions> = {}) => {
    const onSessionExpired = vi.fn();
    const adapter = (config: InternalAxiosRequestConfig) => {
      const url = config.url ?? '';
      sent.push({ tab: name, url, authorization: bearer(config), body: bodyOf(config) });
      const route = routes[`${(config.method ?? 'get').toUpperCase()} ${url}`];
      return route ? route(config, name) : reply(config, 404);
    };
    const client = new ApiClient({
      baseURL: 'http://api.test',
      adapter,
      refreshTimeoutMs: 1000,
      onSessionExpired,
      ...options,
    });
    return { client, api: client.getClient(), onSessionExpired };
  };
  const refreshes = (name?: string) => sent.filter((s) => s.url === '/auth/refresh' && (!name || s.tab === name));
  const calls = (name: string, url: string) => sent.filter((s) => s.tab === name && s.url === url);
  return { sent, tab, refreshes, calls };
}

const storedRecord = () => {
  const raw = localStorage.getItem(SESSION_KEY);
  return raw === null ? null : (JSON.parse(raw) as Record<string, unknown>);
};

const OLD = jwtFor(1, 'old');
const NEW = jwtFor(1, 'new');
const PC = jwtFor(1, 'password-changed');

describe('ApiClient across tabs sharing a remembered session', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const bothOrders = async (first: 'A' | 'B') => {
    const lock = createMemorySessionLock();
    const rejected = { A: deferred<void>(), B: deferred<void>() };
    const refreshGate = deferred<void>();
    let spent = false;
    const browser = createBrowser({
      'GET /items': (config, tab) =>
        bearer(config) === `Bearer ${NEW}`
          ? ok(config, { tab })
          : rejected[tab as 'A' | 'B'].promise.then(() => unauthorized(config)),
      'POST /auth/refresh': (config) =>
        refreshGate.promise.then(() => {
          if (bodyOf(config).refresh_token !== 'r0' || spent) return unauthorized(config);
          spent = true;
          return ok(config, { token: NEW, refresh_token: 'r1' });
        }),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;

    const a = tabA.api.get('/items');
    const b = tabB.api.get('/items');
    await vi.waitFor(() => expect(browser.sent.filter((s) => s.url === '/items')).toHaveLength(2));
    const second = first === 'A' ? 'B' : 'A';
    // The first tab's 401 starts the refresh, which holds the lock...
    rejected[first].resolve();
    await vi.waitFor(() => expect(browser.refreshes(first)).toHaveLength(1));
    // ...and the other tab's 401 arrives while it is on the wire.
    rejected[second].resolve();
    await flush();
    await flush();
    expect(browser.refreshes()).toHaveLength(1);
    refreshGate.resolve();

    await expect(a).resolves.toMatchObject({ data: { tab: 'A' } });
    await expect(b).resolves.toMatchObject({ data: { tab: 'B' } });
    expect(browser.refreshes()).toHaveLength(1);
    expect(browser.refreshes(first)).toHaveLength(1);
    expect(browser.refreshes()[0].body).toEqual({ refresh_token: 'r0' });
    for (const name of ['A', 'B']) {
      expect(browser.calls(name, '/items').map((s) => s.authorization)).toEqual([`Bearer ${OLD}`, `Bearer ${NEW}`]);
    }
    expect(tabA.onSessionExpired).not.toHaveBeenCalled();
    expect(tabB.onSessionExpired).not.toHaveBeenCalled();
    expect(storedRecord()).toMatchObject({ id: sessionId, access: NEW, refresh: 'r1', persist: true });
  };

  it('one refresh renews both tabs when tab A is rejected first', async () => {
    await bothOrders('A');
  });

  it('one refresh renews both tabs when tab B is rejected first', async () => {
    await bothOrders('B');
  });

  it('a tab that acquires the lock after another tab rotated adopts the stored successor without calling /auth/refresh', async () => {
    const lock = createMemorySessionLock();
    const lateRejection = deferred<void>();
    const browser = createBrowser({
      'GET /items': (config, tab) => {
        if (bearer(config) === `Bearer ${NEW}`) return ok(config, { tab });
        return tab === 'B' ? lateRejection.promise.then(() => unauthorized(config)) : unauthorized(config);
      },
      'POST /auth/refresh': (config) => ok(config, { token: NEW, refresh_token: 'r1' }),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;

    // B's request went out with the old access token; A renews meanwhile.
    const b = tabB.api.get('/items');
    await vi.waitFor(() => expect(browser.calls('B', '/items')).toHaveLength(1));
    await expect(tabA.api.get('/items')).resolves.toMatchObject({ data: { tab: 'A' } });
    expect(storedRecord()).toMatchObject({ id: sessionId, access: NEW, refresh: 'r1' });

    lateRejection.resolve();
    await expect(b).resolves.toMatchObject({ data: { tab: 'B' } });
    expect(browser.refreshes('B')).toHaveLength(0);
    expect(browser.refreshes()).toHaveLength(1);
    expect(browser.calls('B', '/items').map((s) => s.authorization)).toEqual([`Bearer ${OLD}`, `Bearer ${NEW}`]);
    expect(lock.acquisitions).toBe(2);
    expect(storedRecord()).toMatchObject({ id: sessionId, access: NEW, refresh: 'r1' });
  });

  it('a refresh lost to another tab that already stored the successor adopts it instead of signing out', async () => {
    // No Web Locks: each tab has its own in-tab lock, so both present r0.
    const gates = { A: deferred<void>(), B: deferred<void>() };
    const browser = createBrowser({
      'GET /items': (config, tab) => (bearer(config) === `Bearer ${NEW}` ? ok(config, { tab }) : unauthorized(config)),
      'POST /auth/refresh': (config, tab) =>
        tab === 'A'
          ? gates.A.promise.then(() => ok(config, { token: NEW, refresh_token: 'r1' }))
          : gates.B.promise.then(() => unauthorized(config)),
    });
    const tabA = browser.tab('A', { sessionLock: createMemorySessionLock() });
    const tabB = browser.tab('B', { sessionLock: createMemorySessionLock() });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;

    const a = tabA.api.get('/items');
    const b = tabB.api.get('/items');
    await vi.waitFor(() => expect(browser.refreshes()).toHaveLength(2));
    expect(browser.refreshes().map((s) => s.body?.refresh_token)).toEqual(['r0', 'r0']);

    // The winner stores the successor first; the loser's 401 arrives after.
    gates.A.resolve();
    await expect(a).resolves.toMatchObject({ data: { tab: 'A' } });
    gates.B.resolve();
    await expect(b).resolves.toMatchObject({ data: { tab: 'B' } });

    expect(browser.calls('B', '/items').map((s) => s.authorization)).toEqual([`Bearer ${OLD}`, `Bearer ${NEW}`]);
    expect(tabB.onSessionExpired).not.toHaveBeenCalled();
    expect(tabA.onSessionExpired).not.toHaveBeenCalled();
    expect(storedRecord()).toMatchObject({ id: sessionId, access: NEW, refresh: 'r1' });
    expect(localStorage.getItem(TOKEN_KEY)).toBe(NEW);
  });

  it('a refresh rejected after another tab signed in another account clears nothing', async () => {
    let gate = deferred<void>();
    const browser = createBrowser({
      'GET /items': unauthorized,
      'POST /auth/refresh': (config) => gate.promise.then(() => unauthorized(config)),
    });
    const tabA = browser.tab('A', { sessionLock: createMemorySessionLock() });
    const tabB = browser.tab('B', { sessionLock: createMemorySessionLock() });

    for (const [signedIn, replacement] of [
      [jwtFor(2, 'other-account'), 'other-r0'],
      [jwtFor(1, 'new-sign-in'), 'new-sign-in-r0'],
    ]) {
      gate = deferred<void>();
      localStorage.clear();
      tabA.client.startSession(OLD, 'r0', true);
      let refreshing = browser.refreshes().length;
      const b = tabB.api.get('/items');
      await vi.waitFor(() => expect(browser.refreshes().length).toBe(refreshing + 1));
      refreshing += 1;

      // Another tab signs in (another account, or the same one anew) while
      // B's refresh of the old session is on the wire.
      tabA.client.startSession(signedIn, replacement, true);
      const replaced = localStorage.getItem(SESSION_KEY);
      gate.resolve();

      await expect(b).rejects.toBeInstanceOf(SessionChangedError);
      expect(localStorage.getItem(SESSION_KEY)).toBe(replaced);
      expect(localStorage.getItem(TOKEN_KEY)).toBe(signedIn);
      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe(replacement);
      expect(tabB.onSessionExpired).not.toHaveBeenCalled();
      expect(browser.refreshes()).toHaveLength(refreshing);
    }
  });

  it('a refresh rejected for the token storage still holds ends the shared session', async () => {
    const lock = createMemorySessionLock();
    const browser = createBrowser({
      'GET /items': unauthorized,
      'POST /auth/refresh': unauthorized,
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);

    await expect(tabA.api.get('/items')).rejects.toMatchObject({ response: { status: 401 } });
    expect(browser.refreshes('A')).toHaveLength(1);
    expect(tabA.onSessionExpired).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();

    // The other tab's next request finds no session: its refresh ends
    // without calling /auth/refresh.
    expect(tabB.client.getToken()).toBeNull();
    await expect(tabB.api.get('/items')).rejects.toThrow('No refresh token available');
    expect(browser.calls('B', '/items')[0].authorization).toBeUndefined();
    expect(browser.refreshes('B')).toHaveLength(0);
    expect(tabB.onSessionExpired).toHaveBeenCalledTimes(1);
    expect(tabA.onSessionExpired).toHaveBeenCalledTimes(1);
  });

  it('a password change after another tab rotated the shared session presents the rotated refresh token', async () => {
    const lock = createMemorySessionLock();
    const refreshGate = deferred<void>();
    let staleNext = false;
    const browser = createBrowser({
      'GET /items': (config, tab) => (bearer(config) === `Bearer ${NEW}` ? ok(config, { tab }) : unauthorized(config)),
      'POST /auth/refresh': (config) => refreshGate.promise.then(() => ok(config, { token: NEW, refresh_token: 'r1' })),
      'POST /auth/change-password': (config) => {
        if (staleNext) return stale(config);
        return bearer(config) === `Bearer ${NEW}` && bodyOf(config).refresh_token === 'r1'
          ? ok(config, { message: 'Password changed successfully', token: PC, refresh_token: 'r-pc' })
          : stale(config);
      },
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;

    // B is rotating the session under the lock when A asks for the change.
    const b = tabB.api.get('/items');
    await vi.waitFor(() => expect(browser.refreshes('B')).toHaveLength(1));
    const change = tabA.client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await flush();
    expect(browser.calls('A', '/auth/change-password')).toHaveLength(0);
    refreshGate.resolve();

    await expect(b).resolves.toMatchObject({ data: { tab: 'B' } });
    await expect(change).resolves.toEqual({ renewed: true });
    const posts = browser.calls('A', '/auth/change-password');
    expect(posts).toHaveLength(1);
    expect(posts[0].authorization).toBe(`Bearer ${NEW}`);
    expect(posts[0].body).toEqual({
      current_password: 'OldStrongPass1!',
      new_password: 'NewStrongPass1!',
      refresh_token: 'r1',
    });
    expect(storedRecord()).toMatchObject({ id: sessionId, access: PC, refresh: 'r-pc' });

    // A 409 SESSION_STALE from the server still surfaces unchanged.
    staleNext = true;
    await expect(tabA.client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).rejects.toMatchObject({
      response: { status: 409, data: { code: 'SESSION_STALE' } },
    });
    expect(browser.calls('A', '/auth/change-password')).toHaveLength(2);
    expect(storedRecord()).toMatchObject({ id: sessionId, access: PC, refresh: 'r-pc' });
  });

  it('a refresh in another tab waits for a password change and adopts its renewed pair', async () => {
    const lock = createMemorySessionLock();
    const changeGate = deferred<void>();
    const browser = createBrowser({
      'GET /items': (config, tab) => (bearer(config) === `Bearer ${PC}` ? ok(config, { tab }) : unauthorized(config)),
      'POST /auth/refresh': (config) => ok(config, { token: NEW, refresh_token: 'r1' }),
      'POST /auth/change-password': (config) =>
        changeGate.promise.then(() =>
          ok(config, { message: 'Password changed successfully', token: PC, refresh_token: 'r-pc' })
        ),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;

    const change = tabA.client.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await vi.waitFor(() => expect(browser.calls('A', '/auth/change-password')).toHaveLength(1));
    const b = tabB.api.get('/items');
    await vi.waitFor(() => expect(browser.calls('B', '/items')).toHaveLength(1));
    await flush();
    // B's refresh is queued behind the change.
    expect(browser.refreshes()).toHaveLength(0);

    changeGate.resolve();
    await expect(change).resolves.toEqual({ renewed: true });
    await expect(b).resolves.toMatchObject({ data: { tab: 'B' } });

    expect(browser.refreshes()).toHaveLength(0);
    expect(browser.calls('B', '/items').map((s) => s.authorization)).toEqual([`Bearer ${OLD}`, `Bearer ${PC}`]);
    expect(tabB.onSessionExpired).not.toHaveBeenCalled();
    expect(storedRecord()).toMatchObject({ id: sessionId, access: PC, refresh: 'r-pc' });
  });

  it('a password change that renews an expired access token takes the session lock once', async () => {
    const lock = createMemorySessionLock();
    const browser = createBrowser({
      'POST /auth/refresh': (config) =>
        bodyOf(config).refresh_token === 'r0' ? ok(config, { token: NEW, refresh_token: 'r1' }) : unauthorized(config),
      'POST /auth/change-password': (config) =>
        bearer(config) === `Bearer ${NEW}` && bodyOf(config).refresh_token === 'r1'
          ? ok(config, { message: 'Password changed successfully', token: PC, refresh_token: 'r-pc' })
          : unauthorized(config),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const sessionId = storedRecord()?.id;
    const before = lock.acquisitions;

    await expect(tabA.client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).resolves.toEqual({
      renewed: true,
    });

    expect(lock.acquisitions - before).toBe(1);
    expect(browser.refreshes()).toHaveLength(1);
    expect(browser.calls('A', '/auth/change-password').map((s) => s.authorization)).toEqual([
      `Bearer ${OLD}`,
      `Bearer ${NEW}`,
    ]);
    expect(storedRecord()).toMatchObject({ id: sessionId, access: PC, refresh: 'r-pc' });
  });

  it('signing out in one tab is not blocked by another tab holding the lock and the late pair is not stored', async () => {
    const lock = createMemorySessionLock();
    const refreshGate = deferred<void>();
    const browser = createBrowser({
      'GET /items': (config, tab) => (bearer(config) === `Bearer ${NEW}` ? ok(config, { tab }) : unauthorized(config)),
      'GET /other': (config, tab) => (bearer(config) === `Bearer ${NEW}` ? ok(config, { tab }) : unauthorized(config)),
      'POST /auth/refresh': (config) => refreshGate.promise.then(() => ok(config, { token: NEW, refresh_token: 'r1' })),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const revoke = vi.fn(async () => {});

    const waiters = Promise.allSettled([tabB.api.get('/items'), tabB.api.get('/other')]);
    await vi.waitFor(() => expect(browser.refreshes('B')).toHaveLength(1));
    expect(lock.acquisitions).toBe(1);

    const signOut = tabA.client.endSession(revoke);
    // Removed at once, without waiting for B's lock.
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
    await expect(signOut).resolves.toBeUndefined();
    expect(revoke).toHaveBeenCalledWith(OLD);

    refreshGate.resolve();
    const results = await waiters;
    for (const result of results) {
      expect(result.status).toBe('rejected');
      expect((result as PromiseRejectedResult).reason).toBeInstanceOf(SessionChangedError);
    }
    for (const store of [localStorage, sessionStorage]) {
      expect(store.getItem(SESSION_KEY)).toBeNull();
      expect(store.getItem(TOKEN_KEY)).toBeNull();
      expect(store.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    }
    expect(browser.sent.filter((s) => s.authorization === `Bearer ${NEW}`)).toHaveLength(0);
  });

  it('a refresh still waiting for the lock after the refresh deadline is rejected and keeps the stored session', async () => {
    vi.useFakeTimers();
    const lock = createMemorySessionLock();
    const browser = createBrowser({
      'GET /items': unauthorized,
      'POST /auth/refresh': (config) => ok(config, { token: NEW, refresh_token: 'r1' }),
    });
    const tabA = browser.tab('A', { sessionLock: lock });
    const tabB = browser.tab('B', { sessionLock: lock });
    tabA.client.startSession(OLD, 'r0', true);
    const stored = localStorage.getItem(SESSION_KEY);

    // Another tab holds the lock for longer than B's refresh deadline.
    const held = deferred<void>();
    const holder = lock.run(() => held.promise);
    const waiters = Promise.allSettled([tabB.api.get('/items'), tabB.api.get('/items')]);
    await vi.advanceTimersByTimeAsync(1000);
    const results = await waiters;

    for (const result of results) {
      expect(result.status).toBe('rejected');
      expect((result as PromiseRejectedResult).reason).toBeInstanceOf(RefreshTimeoutError);
    }
    expect(browser.refreshes()).toHaveLength(0);
    expect(localStorage.getItem(SESSION_KEY)).toBe(stored);
    expect(tabB.onSessionExpired).not.toHaveBeenCalled();

    // The queued holder never runs, even once the lock is free.
    held.resolve();
    await holder;
    await vi.advanceTimersByTimeAsync(10);
    expect(lock.acquisitions).toBe(1);
    expect(browser.refreshes()).toHaveLength(0);
    expect(localStorage.getItem(SESSION_KEY)).toBe(stored);
    expect(tabA.onSessionExpired).not.toHaveBeenCalled();
  });
});
