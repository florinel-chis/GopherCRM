import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import type { AxiosRequestConfig, AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import {
  ApiClient,
  REFRESH_TOKEN_KEY,
  SESSION_KEY,
  SessionChangedError,
  TOKEN_KEY,
  type ApiClientOptions,
  type SessionSnapshot,
} from './client';
import { legacySessionId, sha256Hex } from './sessionRecord';

// The stored session is one record (#184). These tests drive the real
// ApiClient over the browser storages (or instrumented ones) and a custom
// axios adapter, as client.refresh.test.ts does.

type Handler = (config: InternalAxiosRequestConfig) => Promise<AxiosResponse>;

interface Sent {
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
const bearer = (config: InternalAxiosRequestConfig) =>
  AxiosHeaders.from(config.headers).get('Authorization') as string | undefined;
const accepts = (token: string): Handler => (config) =>
  bearer(config) === `Bearer ${token}` ? ok(config, { ok: true }) : unauthorized(config);

const jwtFor = (userId: number, tag: string) => {
  const encode = (value: object) =>
    btoa(JSON.stringify(value)).replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
  return `${encode({ alg: 'HS256', typ: 'JWT' })}.${encode({ user_id: userId, tag })}.signature`;
};

function makeTab(routes: Record<string, Handler>, options: Partial<ApiClientOptions> = {}) {
  const sent: Sent[] = [];
  const onSessionExpired = vi.fn();
  const adapter = (config: InternalAxiosRequestConfig) => {
    const url = config.url ?? '';
    const body = typeof config.data === 'string' && config.data ? JSON.parse(config.data) : undefined;
    sent.push({ url, authorization: bearer(config), body });
    const handler = routes[`${(config.method ?? 'get').toUpperCase()} ${url}`];
    return handler ? handler(config) : reply(config, 404);
  };
  const client = new ApiClient({
    baseURL: 'http://api.test',
    adapter,
    refreshTimeoutMs: 1000,
    onSessionExpired,
    ...options,
  });
  const refreshes = () => sent.filter((s) => s.url === '/auth/refresh');
  return { client, api: client.getClient(), sent, refreshes, onSessionExpired };
}

/** A Storage that logs every write and can run a hook after each setItem. */
class MemoryStorage {
  private items = new Map<string, string>();
  log: string[] = [];
  afterSet: ((key: string) => void) | null = null;

  get length(): number {
    return this.items.size;
  }
  key(index: number): string | null {
    return [...this.items.keys()][index] ?? null;
  }
  getItem(key: string): string | null {
    return this.items.get(key) ?? null;
  }
  setItem(key: string, value: string): void {
    this.items.set(key, String(value));
    this.log.push(`set ${key}`);
    this.afterSet?.(key);
  }
  removeItem(key: string): void {
    this.items.delete(key);
    this.log.push(`remove ${key}`);
  }
  clear(): void {
    this.items.clear();
  }
}

const instrumented = () => {
  const local = new MemoryStorage();
  const session = new MemoryStorage();
  return {
    local,
    session,
    storage: { local: local as unknown as Storage, session: session as unknown as Storage },
  };
};

const recordIn = (store: Pick<Storage, 'getItem'>) => {
  const raw = store.getItem(SESSION_KEY);
  return raw === null ? null : (JSON.parse(raw) as Record<string, unknown>);
};

const writeRawRecord = (store: Storage, record: Record<string, unknown>) => {
  store.setItem(SESSION_KEY, JSON.stringify(record));
  if (typeof record.access === 'string') store.setItem(TOKEN_KEY, record.access);
  if (typeof record.refresh === 'string') store.setItem(REFRESH_TOKEN_KEY, record.refresh);
};

const KEYS = [SESSION_KEY, TOKEN_KEY, REFRESH_TOKEN_KEY];

describe('ApiClient session record', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it('stores both tokens, the account, the persistence choice and a session id in one record in the chosen storage', () => {
    const { client } = makeTab({});
    const randomSpy = vi.spyOn(globalThis.crypto, 'getRandomValues');
    // A predecessor in the other storage, written by a non-remembered sign-in.
    client.startSession(jwtFor(6, 'before'), 'r-before', false);
    randomSpy.mockClear();

    const access = jwtFor(7, 'remembered');
    client.startSession(access, 'r0', true);

    expect(randomSpy).toHaveBeenCalledTimes(1);
    randomSpy.mockRestore();
    const remembered = recordIn(localStorage);
    expect(remembered).toEqual({
      v: 1,
      id: expect.stringMatching(/^[0-9a-f]{32}$/),
      access,
      refresh: 'r0',
      subject: '7',
      persist: true,
    });
    expect(Object.keys(remembered ?? {}).sort()).toEqual(['access', 'id', 'persist', 'refresh', 'subject', 'v']);
    for (const key of KEYS) {
      expect(sessionStorage.getItem(key)).toBeNull();
    }

    // Not remembered: sessionStorage, and no refresh token is stored as null.
    client.startSession('not-a-jwt', undefined, false);
    const tabOnly = recordIn(sessionStorage);
    expect(tabOnly).toEqual({
      v: 1,
      id: expect.stringMatching(/^[0-9a-f]{32}$/),
      access: 'not-a-jwt',
      refresh: null,
      subject: null,
      persist: false,
    });
    expect(tabOnly?.id).not.toBe(remembered?.id);
    for (const key of KEYS) {
      expect(localStorage.getItem(key)).toBeNull();
    }
  });

  it('a session without a refresh token never takes one from the other storage', async () => {
    const { client, api, refreshes, onSessionExpired } = makeTab({
      'GET /items': unauthorized,
      'POST /auth/refresh': (config) => ok(config, { token: 'stolen', refresh_token: 'stolen-r' }),
    });
    const access = jwtFor(1, 'tab');
    client.startSession(access, undefined, false);
    // Another session in localStorage, as a record and as legacy keys.
    writeRawRecord(localStorage, {
      v: 1,
      id: 'other-session',
      access: jwtFor(1, 'other'),
      refresh: 'other-refresh',
      subject: '1',
      persist: true,
    });

    expect(client.getToken()).toBe(access);
    expect(client.getRefreshToken()).toBeNull();
    expect(client.captureSession().refreshToken).toBeNull();

    await expect(api.get('/items')).rejects.toThrow('No refresh token available');
    expect(refreshes()).toHaveLength(0);
    expect(onSessionExpired).toHaveBeenCalledTimes(1);
  });

  it('uses the sessionStorage record whole when localStorage holds another session', async () => {
    const tabAccess = jwtFor(1, 'tab');
    const tabRenewed = jwtFor(1, 'tab-renewed');
    const { client, api, sent } = makeTab({
      'GET /items': accepts(tabRenewed),
      'POST /auth/refresh': (config) => ok(config, { token: tabRenewed, refresh_token: 'tab-r1' }),
    });
    client.startSession(tabAccess, 'tab-r0', false);
    const tabId = recordIn(sessionStorage)?.id;
    const remembered = {
      v: 1,
      id: 'remembered-session',
      access: jwtFor(2, 'remembered'),
      refresh: 'remembered-refresh',
      subject: '2',
      persist: true,
    };
    writeRawRecord(localStorage, remembered);

    expect(client.getToken()).toBe(tabAccess);
    expect(client.getRefreshToken()).toBe('tab-r0');
    expect(client.captureSession()).toMatchObject({ id: tabId, subject: '1', refreshToken: 'tab-r0' });

    await expect(api.get('/items')).resolves.toMatchObject({ data: { ok: true } });
    expect(sent.map((s) => s.authorization ?? s.body?.refresh_token)).toEqual([
      `Bearer ${tabAccess}`,
      'tab-r0',
      `Bearer ${tabRenewed}`,
    ]);
    // The renewed pair stays in the tab's storage and record; the remembered
    // session is untouched.
    expect(recordIn(sessionStorage)).toMatchObject({ id: tabId, access: tabRenewed, refresh: 'tab-r1', persist: false });
    expect(recordIn(localStorage)).toEqual(remembered);
  });

  it('mirrors the tokens under the legacy keys of the same storage and never reads them while a record exists', async () => {
    const { local, session, storage } = instrumented();
    const access = jwtFor(1, 'a');
    const renewed = jwtFor(1, 'renewed');
    const { client, api, sent } = makeTab(
      {
        'GET /items': accepts(renewed),
        'POST /auth/refresh': (config) => ok(config, { token: renewed, refresh_token: 'r1' }),
      },
      { storage }
    );

    client.startSession(jwtFor(1, 'no-refresh'), undefined, true);
    expect(local.log.slice(-3)).toEqual([`set ${SESSION_KEY}`, `set ${TOKEN_KEY}`, `remove ${REFRESH_TOKEN_KEY}`]);

    local.log = [];
    client.startSession(access, 'r0', true);
    expect(local.log.filter((entry) => entry.startsWith('set '))).toEqual([
      `set ${SESSION_KEY}`,
      `set ${TOKEN_KEY}`,
      `set ${REFRESH_TOKEN_KEY}`,
    ]);
    expect(local.getItem(TOKEN_KEY)).toBe(access);
    expect(local.getItem(REFRESH_TOKEN_KEY)).toBe('r0');
    expect(session.log.filter((entry) => entry.startsWith('set '))).toEqual([]);

    // Changing the mirrors changes neither the bearer nor the refresh token.
    local.setItem(TOKEN_KEY, 'tampered-access');
    local.setItem(REFRESH_TOKEN_KEY, 'tampered-refresh');
    expect(client.getToken()).toBe(access);
    expect(client.getRefreshToken()).toBe('r0');

    local.log = [];
    await expect(api.get('/items')).resolves.toMatchObject({ data: { ok: true } });
    expect(sent[0].authorization).toBe(`Bearer ${access}`);
    expect(sent[1]).toMatchObject({ url: '/auth/refresh', body: { refresh_token: 'r0' } });
    expect(local.log).toEqual([`set ${SESSION_KEY}`, `set ${TOKEN_KEY}`, `set ${REFRESH_TOKEN_KEY}`]);
    expect(local.getItem(TOKEN_KEY)).toBe(renewed);
    expect(local.getItem(REFRESH_TOKEN_KEY)).toBe('r1');
  });

  it('migrates a legacy two-key session from one storage into a record', () => {
    const access = jwtFor(4, 'legacy');
    localStorage.setItem(TOKEN_KEY, access);
    localStorage.setItem(REFRESH_TOKEN_KEY, 'legacy-refresh');
    const tabA = makeTab({});

    expect(tabA.client.getToken()).toBe(access);
    const migrated = recordIn(localStorage);
    expect(migrated).toEqual({
      v: 1,
      id: `legacy-${sha256Hex('legacy-refresh')}`,
      access,
      refresh: 'legacy-refresh',
      subject: '4',
      persist: true,
    });
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();

    // Migrated once: another tab reads the record, and a concurrent
    // migration of the same legacy session derives the same id.
    const tabB = makeTab({});
    expect(tabB.client.captureSession().id).toBe(migrated?.id);
    localStorage.removeItem(SESSION_KEY);
    expect(tabB.client.captureSession().id).toBe(migrated?.id);

    // sessionStorage first; its own refresh token or none, never the other's.
    localStorage.clear();
    sessionStorage.setItem(TOKEN_KEY, 'tab-access');
    localStorage.setItem(TOKEN_KEY, 'remembered-access');
    localStorage.setItem(REFRESH_TOKEN_KEY, 'remembered-refresh');
    expect(tabA.client.getToken()).toBe('tab-access');
    expect(tabA.client.getRefreshToken()).toBeNull();
    expect(recordIn(sessionStorage)).toEqual({
      v: 1,
      id: legacySessionId('tab-access', null),
      access: 'tab-access',
      refresh: null,
      subject: null,
      persist: false,
    });
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
  });

  it('drops a legacy refresh token that has no access token in its storage', () => {
    localStorage.setItem(REFRESH_TOKEN_KEY, 'orphan-refresh');
    const { client } = makeTab({});

    expect(client.getToken()).toBeNull();
    expect(client.getRefreshToken()).toBeNull();
    expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();

    // An orphan in sessionStorage does not attach itself to the remembered pair.
    sessionStorage.setItem(REFRESH_TOKEN_KEY, 'tab-orphan');
    localStorage.setItem(TOKEN_KEY, 'remembered-access');
    localStorage.setItem(REFRESH_TOKEN_KEY, 'remembered-refresh');
    expect(client.getRefreshToken()).toBe('remembered-refresh');
    expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    expect(recordIn(localStorage)).toMatchObject({ access: 'remembered-access', refresh: 'remembered-refresh' });
  });

  it('derives the migrated session id from a SHA-256 digest that does not contain the token', () => {
    // FIPS 180-2 test vectors, the last one spanning two blocks.
    expect(sha256Hex('')).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855');
    expect(sha256Hex('abc')).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');
    const long = 'abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq';
    expect(sha256Hex(long)).toBe('248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1');

    const { client } = makeTab({});
    // With a refresh token, the id is the digest of the refresh token.
    localStorage.setItem(TOKEN_KEY, 'opaque-access');
    localStorage.setItem(REFRESH_TOKEN_KEY, 'abc');
    expect(client.captureSession().id).toBe('legacy-ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad');

    // Without one, the digest of the access token.
    localStorage.clear();
    sessionStorage.setItem(TOKEN_KEY, long);
    const id = client.captureSession().id;
    expect(id).toBe('legacy-248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1');
    expect(id).toMatch(/^legacy-[0-9a-f]{64}$/);
    expect(id).not.toContain(long);
  });

  it('does not migrate legacy keys while a record exists in either storage', () => {
    const { client } = makeTab({});

    // A valid record in sessionStorage, legacy keys in localStorage.
    client.startSession('tab-access', 'tab-refresh', false);
    localStorage.setItem(TOKEN_KEY, 'legacy-access');
    localStorage.setItem(REFRESH_TOKEN_KEY, 'legacy-refresh');
    expect(client.getToken()).toBe('tab-access');
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBe('legacy-access');

    // A record missing its mirror in localStorage, legacy keys in sessionStorage.
    localStorage.clear();
    sessionStorage.clear();
    localStorage.setItem(
      SESSION_KEY,
      JSON.stringify({ v: 1, id: 'unmirrored', access: 'a', refresh: 'r', subject: null, persist: true })
    );
    sessionStorage.setItem(TOKEN_KEY, 'legacy-tab-access');
    expect(client.getToken()).toBeNull();
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
    expect(localStorage.getItem(SESSION_KEY)).not.toBeNull();

    // A malformed record not yet removed: the read that removes it does not
    // migrate either.
    localStorage.clear();
    localStorage.setItem(SESSION_KEY, '{"v":1');
    expect(client.getToken()).toBeNull();
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
  });

  it('removing the legacy access-token key ends the stored session', async () => {
    const { client, api, sent } = makeTab({ 'GET /items': (config) => ok(config, { ok: true }) });
    client.startSession(jwtFor(1, 'a'), 'r0', true);
    const stored = localStorage.getItem(SESSION_KEY);
    // Would be migrated if a record-less read were allowed to.
    sessionStorage.setItem(TOKEN_KEY, 'legacy-tab-access');

    // How the protected browser specs sign out.
    localStorage.removeItem(TOKEN_KEY);

    expect(client.getToken()).toBeNull();
    expect(client.getRefreshToken()).toBeNull();
    expect(client.captureSession().id).toBeNull();
    // The read removed nothing and migrated nothing.
    expect(localStorage.getItem(SESSION_KEY)).toBe(stored);
    expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();

    await api.get('/items');
    expect(sent[0].authorization).toBeUndefined();

    client.clearTokens();
    expect(localStorage.getItem(SESSION_KEY)).toBeNull();
  });

  it('treats a malformed record as no session and removes it', () => {
    const { client } = makeTab({});
    const malformed = [
      'not json',
      JSON.stringify({ v: 2, id: 'x', access: 'a', refresh: 'r', subject: null, persist: true }),
      JSON.stringify({ v: 1, access: 'a', refresh: 'r', subject: null, persist: true }),
      JSON.stringify({ v: 1, id: 7, access: 'a', refresh: 'r', subject: null, persist: true }),
      JSON.stringify({ v: 1, id: 'x', access: null, refresh: 'r', subject: null, persist: true }),
      'null',
    ];
    for (const raw of malformed) {
      for (const store of [localStorage, sessionStorage]) {
        store.setItem(SESSION_KEY, raw);
        store.setItem(TOKEN_KEY, 'mirror-access');
        store.setItem(REFRESH_TOKEN_KEY, 'mirror-refresh');

        expect(client.getToken()).toBeNull();
        for (const key of KEYS) {
          expect(store.getItem(key)).toBeNull();
        }
      }
    }
  });

  it('clearTokens removes the record and the legacy keys from both storages and starts a new generation', () => {
    const { client } = makeTab({});
    client.startSession('remembered', 'remembered-r', true);
    writeRawRecord(sessionStorage, {
      v: 1,
      id: 'tab',
      access: 'tab-access',
      refresh: 'tab-refresh',
      subject: null,
      persist: false,
    });
    const generation = client.getSessionGeneration();
    const before = client.captureSession();

    client.clearTokens();

    for (const store of [localStorage, sessionStorage]) {
      for (const key of KEYS) {
        expect(store.getItem(key)).toBeNull();
      }
    }
    expect(client.getSessionGeneration()).toBe(generation + 1);
    expect(client.isCurrentSession(before)).toBe(false);
    expect(client.getToken()).toBeNull();
  });

  it('a refresh rotation rewrites the whole record and keeps the session id', async () => {
    const { local, session, storage } = instrumented();
    const access = jwtFor(3, 'a');
    const { client, api } = makeTab(
      {
        'GET /items': accepts('opaque-renewed'),
        // The renewed access token carries no readable claim: the stored
        // account must not change with it.
        'POST /auth/refresh': (config) => ok(config, { token: 'opaque-renewed', refresh_token: 'r1' }),
        'POST /auth/change-password': (config) =>
          ok(config, { message: 'Password changed successfully', token: 'opaque-pc', refresh_token: 'r-pc' }),
      },
      { storage }
    );
    client.startSession(access, 'r0', false);
    const original = recordIn(session);

    session.log = [];
    await api.get('/items');
    expect(session.log.filter((entry) => entry === `set ${SESSION_KEY}`)).toHaveLength(1);
    expect(recordIn(session)).toEqual({
      v: 1,
      id: original?.id,
      access: 'opaque-renewed',
      refresh: 'r1',
      subject: '3',
      persist: false,
    });

    session.log = [];
    await expect(client.changePassword('OldStrongPass1!', 'NewStrongPass1!')).resolves.toEqual({ renewed: true });
    expect(session.log.filter((entry) => entry === `set ${SESSION_KEY}`)).toHaveLength(1);
    expect(recordIn(session)).toEqual({
      v: 1,
      id: original?.id,
      access: 'opaque-pc',
      refresh: 'r-pc',
      subject: '3',
      persist: false,
    });
    expect(local.getItem(SESSION_KEY)).toBeNull();
  });

  it('a read between the record write and the mirror write deletes nothing and migrates nothing', async () => {
    const { local, session, storage } = instrumented();
    const tabA = makeTab({}, { storage });
    const tabB = makeTab(
      {
        'GET /items': unauthorized,
        'POST /auth/refresh': unauthorized,
      },
      { storage }
    );
    tabA.client.startSession(jwtFor(1, 'first'), 'r-first', true);

    const seen: Array<{
      token: string | null;
      refresh: string | null;
      snapshot: SessionSnapshot;
      record: string | null;
      tabRecord: string | null;
    }> = [];
    local.afterSet = (key) => {
      if (key !== SESSION_KEY || seen.length > 0) return;
      // A stray legacy key a record-less read would migrate.
      session.setItem(TOKEN_KEY, 'stray-legacy');
      seen.push({
        token: tabB.client.getToken(),
        refresh: tabB.client.getRefreshToken(),
        snapshot: tabB.client.captureSession(),
        record: local.getItem(SESSION_KEY),
        tabRecord: session.getItem(SESSION_KEY),
      });
    };

    const access = jwtFor(2, 'second');
    tabA.client.startSession(access, 'r-second', true);
    local.afterSet = null;

    expect(seen).toHaveLength(1);
    const inWindow = seen[0];
    expect(inWindow.token).toBeNull();
    expect(inWindow.refresh).toBeNull();
    expect(inWindow.snapshot.id).toBeNull();
    expect(inWindow.record).not.toBeNull();
    expect(inWindow.tabRecord).toBeNull();

    const newId = recordIn(local)?.id;
    expect(newId).toMatch(/^[0-9a-f]{32}$/);
    expect(tabB.client.getToken()).toBe(access);
    expect(tabB.client.captureSession().id).toBe(newId);
    expect(session.getItem(SESSION_KEY)).toBeNull();

    // An operation captured in the window fails later: nothing is cleared.
    await expect(
      tabB.api.get('/items', { _session: inWindow.snapshot } as unknown as AxiosRequestConfig)
    ).rejects.toBeInstanceOf(SessionChangedError);
    expect(tabB.refreshes()).toHaveLength(0);
    expect(tabB.onSessionExpired).not.toHaveBeenCalled();
    expect(recordIn(local)).toMatchObject({ id: newId, access, refresh: 'r-second' });
    expect(local.getItem(TOKEN_KEY)).toBe(access);
  });
});
