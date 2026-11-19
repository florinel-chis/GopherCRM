import { describe, it, expect, beforeEach } from 'vitest';
import { AxiosError, AxiosHeaders } from 'axios';
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import { ApiClient } from './client';

// The public client (#383) serves endpoints answered without a session. These
// tests drive the real ApiClient over a custom axios adapter and injected
// storages, as client.sessionRecord.test.ts does.

type Handler = (config: InternalAxiosRequestConfig) => Promise<AxiosResponse>;

interface Sent {
  method: string;
  url: string;
  baseURL?: string;
  authorization?: string;
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
const ok = (config: InternalAxiosRequestConfig, data: unknown, meta?: unknown) =>
  reply(config, 200, { success: true, data, ...(meta ? { meta } : {}) });

class MemoryStorage {
  private items = new Map<string, string>();
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
  }
  removeItem(key: string): void {
    this.items.delete(key);
  }
  clear(): void {
    this.items.clear();
  }
}

function makeTab(routes: Record<string, Handler>) {
  const sent: Sent[] = [];
  const adapter = (config: InternalAxiosRequestConfig) => {
    const url = config.url ?? '';
    const method = (config.method ?? 'get').toUpperCase();
    sent.push({
      method,
      url,
      baseURL: config.baseURL,
      authorization: AxiosHeaders.from(config.headers).get('Authorization') as string | undefined,
    });
    const handler = routes[`${method} ${url}`];
    return handler ? handler(config) : reply(config, 404);
  };
  const client = new ApiClient({
    baseURL: 'http://api.test',
    timeout: 4321,
    adapter,
    refreshTimeoutMs: 1000,
    onSessionExpired: () => {},
    storage: {
      local: new MemoryStorage() as unknown as Storage,
      session: new MemoryStorage() as unknown as Storage,
    },
  });
  return { client, sent };
}

describe('ApiClient.getPublicClient', () => {
  let tab: ReturnType<typeof makeTab>;

  beforeEach(() => {
    tab = makeTab({
      'GET /quotes/public/response': (config) => ok(config, { number: 'Q-000042' }, { request_id: 'r-1' }),
      'GET /quotes': (config) => ok(config, []),
      'GET /gone': (config) =>
        reply(config, 400, {
          success: false,
          error: { code: 'BAD_REQUEST', message: 'Invalid or expired quote link' },
        }),
      'GET /guarded': (config) =>
        reply(config, 401, { success: false, error: { code: 'UNAUTHORIZED', message: 'Unauthorized' } }),
      'POST /auth/refresh': (config) => ok(config, { token: 'next-access', refresh_token: 'next-refresh' }),
    });
  });

  it('sends no Authorization header while a session is stored; getClient() sends the bearer token', async () => {
    tab.client.startSession('access-1', 'refresh-1', true);

    await tab.client.getPublicClient().get('/quotes/public/response', { headers: { 'X-Quote-Token': 't' } });
    await tab.client.getClient().get('/quotes');

    expect(tab.sent).toHaveLength(2);
    expect(tab.sent[0].url).toBe('/quotes/public/response');
    expect(tab.sent[0].authorization).toBeUndefined();
    // Control: the same client, the same stored session, the session instance.
    expect(tab.sent[1].url).toBe('/quotes');
    expect(tab.sent[1].authorization).toBe('Bearer access-1');
  });

  it('shares the base URL and timeout of the session client and is created once', () => {
    const publicClient = tab.client.getPublicClient();

    expect(publicClient).toBe(tab.client.getPublicClient());
    expect(publicClient).not.toBe(tab.client.getClient());
    expect(publicClient.defaults.baseURL).toBe('http://api.test');
    expect(publicClient.defaults.timeout).toBe(4321);
  });

  it('unwraps the success envelope and keeps its meta', async () => {
    const response = await tab.client.getPublicClient().get('/quotes/public/response');

    expect(response.data).toEqual({ number: 'Q-000042' });
    expect((response as AxiosResponse & { meta?: unknown }).meta).toEqual({ request_id: 'r-1' });
  });

  it('unwraps an error envelope flat', async () => {
    const error = await tab.client
      .getPublicClient()
      .get('/gone')
      .catch((e: AxiosError) => e);

    expect(error).toBeInstanceOf(AxiosError);
    expect((error as AxiosError).response?.status).toBe(400);
    expect((error as AxiosError).response?.data).toEqual({
      code: 'BAD_REQUEST',
      message: 'Invalid or expired quote link',
    });
  });

  it('neither refreshes nor replays on a 401, and leaves the stored session alone', async () => {
    tab.client.startSession('access-1', 'refresh-1', true);

    const error = await tab.client
      .getPublicClient()
      .get('/guarded')
      .catch((e: AxiosError) => e);

    expect((error as AxiosError).response?.status).toBe(401);
    expect(tab.sent.map((s) => `${s.method} ${s.url}`)).toEqual(['GET /guarded']);
    expect(tab.client.getToken()).toBe('access-1');
    expect(tab.client.getRefreshToken()).toBe('refresh-1');
  });
});
