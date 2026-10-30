import axios, { AxiosError } from 'axios';
import type { AxiosAdapter, AxiosInstance, AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import type { APIError, ChangePasswordResponse } from '@/types';

// The `meta` block of the backend envelope. Only paginated list endpoints fill
// anything beyond the request id.
export interface ApiMeta {
  request_id?: string;
  page?: number;
  per_page?: number;
  total?: number;
  total_pages?: number;
}

// The response interceptor replaces `response.data` with the envelope payload,
// which would otherwise throw the pagination totals away. They are re-attached
// to the response itself; endpoint modules that need them cast to this type.
export type ApiResponseWithMeta<T> = AxiosResponse<T> & { meta?: ApiMeta };

interface ApiErrorEnvelope {
  success: false;
  error: APIError;
  meta?: ApiMeta;
}

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || 'http://localhost:8080/api/v1';
const API_TIMEOUT = Number(import.meta.env.VITE_API_TIMEOUT) || 30000;

export const TOKEN_KEY = 'gophercrm_token';
export const REFRESH_TOKEN_KEY = 'gophercrm_refresh_token';

// A refresh that has not answered within this time settles every request
// waiting on it. The deadline rejects on its own: aborting the HTTP call is
// only cleanup, because an adapter that ignores the abort would otherwise keep
// every waiter pending forever.
export const REFRESH_TIMEOUT_MS = 15000;

/**
 * The session a request (or a refresh) belonged to ended while it was in
 * flight: logout, a sign-in to another account, or the session expiring. The
 * late result is discarded and never replayed under the new session.
 */
export class SessionChangedError extends Error {
  constructor() {
    super('The session changed while the request was in flight');
    this.name = 'SessionChangedError';
  }
}

export class RefreshTimeoutError extends Error {
  constructor() {
    super('Session renewal timed out');
    this.name = 'RefreshTimeoutError';
  }
}

type SessionRequestConfig = InternalAxiosRequestConfig & {
  _retry?: boolean;
  _sessionGeneration?: number;
};

interface TokenPair {
  token: string;
  refresh_token: string;
}

export interface ApiClientOptions {
  baseURL?: string;
  timeout?: number;
  /** Transport for both the API and the refresh calls (tests inject one). */
  adapter?: AxiosAdapter;
  refreshTimeoutMs?: number;
  /** Called once when a session ends because it can no longer be renewed. */
  onSessionExpired?: () => void;
}

const defaultSessionExpired = () => {
  if (window.location.pathname !== '/login') {
    window.location.href = '/login';
  }
};

const statusOf = (error: unknown): number | undefined =>
  (error as AxiosError | undefined)?.response?.status;

/**
 * The HTTP client and the owner of the browser session.
 *
 * Session rules:
 * - A session has a generation number. Signing in, signing out and an expired
 *   session start a new generation; a refresh or a password change renews
 *   the current one. Every request is stamped with the generation it was sent
 *   under and is only ever replayed under that same generation.
 * - One refresh at a time: every request that hits a 401 waits on the same
 *   in-flight refresh and is resolved or rejected with it. Refreshes and
 *   session-replacing operations (password change) are serialised through one
 *   lock, so they never overlap in this tab.
 * - A refresh rejected with 400/401 ends the session; a timeout, network
 *   failure or 5xx keeps the stored tokens (the server may still accept them,
 *   or may already have rotated them — the next attempt will tell).
 *
 * Coordinating refreshes across several tabs that share a remembered session
 * is not handled here (#184).
 */
export class ApiClient {
  private client: AxiosInstance;
  private sessionHttp: AxiosInstance;
  private refreshTimeoutMs: number;
  private onSessionExpired: () => void;
  private generation = 0;
  private refreshInFlight: { generation: number; promise: Promise<string> } | null = null;
  private lockTail: Promise<void> = Promise.resolve();

  constructor(options: ApiClientOptions = {}) {
    const baseURL = options.baseURL ?? API_BASE_URL;
    this.refreshTimeoutMs = options.refreshTimeoutMs ?? REFRESH_TIMEOUT_MS;
    this.onSessionExpired = options.onSessionExpired ?? defaultSessionExpired;
    this.client = axios.create({
      baseURL,
      timeout: options.timeout ?? API_TIMEOUT,
      headers: {
        'Content-Type': 'application/json',
      },
      ...(options.adapter ? { adapter: options.adapter } : {}),
    });
    // Session calls (refresh, logout) go through a bare instance: no
    // interceptors, so no Authorization header from the stored session and no
    // refresh loop on their own 401s.
    this.sessionHttp = axios.create({
      baseURL,
      headers: { 'Content-Type': 'application/json' },
      ...(options.adapter ? { adapter: options.adapter } : {}),
    });

    this.setupInterceptors();
  }

  private setupInterceptors() {
    this.client.interceptors.request.use(
      (config: InternalAxiosRequestConfig) => {
        const sessionConfig = config as SessionRequestConfig;
        if (sessionConfig._sessionGeneration === undefined) {
          sessionConfig._sessionGeneration = this.generation;
        }
        const token = this.getToken();
        if (token && config.headers && !config.headers.Authorization) {
          config.headers.Authorization = `Bearer ${token}`;
        }
        return config;
      },
      (error) => {
        return Promise.reject(error);
      }
    );

    this.client.interceptors.response.use(
      (response) => {
        // Unwrap the data from the backend's response format
        if (response.data && response.data.success && response.data.data !== undefined) {
          const envelope = response.data as { data: unknown; meta?: ApiMeta };
          response.data = envelope.data;
          // Keep the envelope metadata reachable: list endpoints report their
          // total row count there and nowhere else.
          (response as ApiResponseWithMeta<unknown>).meta = envelope.meta;
        }
        return response;
      },
      async (error: AxiosError<APIError | ApiErrorEnvelope>) => {
        const config = error.config as SessionRequestConfig | undefined;

        // Auth endpoints never trigger a refresh: a 401 on login/register
        // must show the error, and the session-owning calls under /auth/
        // (change-password, logout) recover on their own.
        const isAuthEndpoint = config?.url?.includes('/auth/');
        if (config && error.response?.status === 401 && !config._retry && !isAuthEndpoint) {
          return this.recoverFromUnauthorized(config);
        }

        // Unwrap error response if it's in the wrapped format
        if (error.response?.data && 'success' in error.response.data && 'error' in error.response.data) {
          error.response.data = error.response.data.error;
        }

        return Promise.reject(error);
      }
    );
  }

  private async recoverFromUnauthorized(config: SessionRequestConfig): Promise<AxiosResponse> {
    const generation = config._sessionGeneration ?? this.generation;
    if (generation !== this.generation) {
      // Sent under a session that has since ended or been replaced.
      throw new SessionChangedError();
    }
    const sentHeader = config.headers?.Authorization;
    const sentToken = typeof sentHeader === 'string' ? sentHeader.replace(/^Bearer /, '') : undefined;

    const token = await this.refreshFor(generation, sentToken);
    if (generation !== this.generation) {
      throw new SessionChangedError();
    }
    // Every replay is marked, so a second 401 is final.
    const replay: SessionRequestConfig = { ...config, _retry: true, _sessionGeneration: generation };
    replay.headers = config.headers;
    replay.headers.Authorization = `Bearer ${token}`;
    return this.client(replay);
  }

  /** Joins the in-flight refresh of this generation, or starts one. */
  private refreshFor(generation: number, sentToken?: string): Promise<string> {
    if (this.refreshInFlight && this.refreshInFlight.generation === generation) {
      return this.refreshInFlight.promise;
    }
    const promise = this.withLock(() => this.performRefresh(generation, sentToken));
    const entry = { generation, promise };
    this.refreshInFlight = entry;
    promise
      .finally(() => {
        if (this.refreshInFlight === entry) {
          this.refreshInFlight = null;
        }
      })
      .catch(() => undefined);
    return promise;
  }

  /**
   * Serialises refreshes and session replacements. A rejected step does not
   * break the chain.
   */
  private withLock<T>(fn: () => Promise<T>): Promise<T> {
    const run = this.lockTail.then(fn, fn);
    this.lockTail = run.then(
      () => undefined,
      () => undefined
    );
    return run;
  }

  /**
   * Renews the session of `generation`. Must only run while holding the lock;
   * it never takes the lock itself, so a lock holder (the password change)
   * can call it without waiting on work queued behind it.
   */
  private async performRefresh(generation: number, sentToken?: string): Promise<string> {
    if (generation !== this.generation) {
      throw new SessionChangedError();
    }
    // A refresh that completed while this one waited for the lock already
    // renewed the session the failed request was sent under.
    const current = this.getToken();
    if (sentToken && current && current !== sentToken) {
      return current;
    }

    const presented = this.getRefreshToken();
    if (!presented) {
      this.expireSession(generation);
      throw new Error('No refresh token available');
    }
    const persist = this.isPersistent();

    let pair: TokenPair;
    try {
      pair = await this.requestRefresh(presented);
    } catch (error) {
      if (generation !== this.generation) {
        throw new SessionChangedError();
      }
      const status = statusOf(error);
      if (status === 400 || status === 401) {
        this.expireSession(generation);
      }
      throw error;
    }

    // The session ended, or this tab's tokens were replaced, while the
    // refresh was in flight: the late pair must not resurrect or overwrite.
    if (generation !== this.generation || this.getRefreshToken() !== presented) {
      throw new SessionChangedError();
    }
    this.setToken(pair.token, persist);
    this.setRefreshToken(pair.refresh_token, persist);
    return pair.token;
  }

  /** POST /auth/refresh under the refresh deadline; writes nothing. */
  private async requestRefresh(refreshToken: string): Promise<TokenPair> {
    const response = await this.withDeadline((signal) =>
      this.sessionHttp.post('/auth/refresh', { refresh_token: refreshToken }, { signal })
    );
    // The bare instance does not unwrap the { success, data } envelope.
    const payload = (response.data?.data ?? response.data) as Partial<TokenPair> | undefined;
    if (!payload?.token || !payload.refresh_token) {
      // The presented token is spent either way; a response without a
      // complete replacement cannot continue the session.
      throw new AxiosError('Refresh response did not carry a token pair', 'ERR_BAD_RESPONSE', undefined, undefined, {
        ...response,
        status: 401,
      });
    }
    return { token: payload.token, refresh_token: payload.refresh_token };
  }

  private async withDeadline<T>(call: (signal: AbortSignal) => Promise<T>): Promise<T> {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const deadline = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        controller.abort();
        reject(new RefreshTimeoutError());
      }, this.refreshTimeoutMs);
    });
    try {
      return await Promise.race([call(controller.signal), deadline]);
    } finally {
      clearTimeout(timer);
    }
  }

  private expireSession(generation: number): void {
    if (generation !== this.generation) {
      return;
    }
    this.clearTokens();
    this.onSessionExpired();
  }

  /**
   * Changes the password and keeps this session renewable: the current
   * refresh token goes with the request and the replacement pair the server
   * returns is stored in the same storage (remember-me choice). Runs under
   * the session lock, after any in-flight refresh. An expired access token is
   * renewed once and the request rebuilt from the renewed pair.
   */
  public async changePassword(currentPassword: string, newPassword: string): Promise<{ renewed: boolean }> {
    const generation = this.generation;
    return this.withLock(async () => {
      if (generation !== this.generation) {
        throw new SessionChangedError();
      }
      const persist = this.isPersistent();
      const send = (refreshToken: string | null) =>
        this.client.post<ChangePasswordResponse>(
          '/auth/change-password',
          {
            current_password: currentPassword,
            new_password: newPassword,
            ...(refreshToken ? { refresh_token: refreshToken } : {}),
          },
          { _sessionGeneration: generation } as Partial<SessionRequestConfig>
        );

      let presented = this.getRefreshToken();
      let response: AxiosResponse<ChangePasswordResponse>;
      try {
        response = await send(presented);
      } catch (error) {
        const status = statusOf(error);
        if (status === 401 && presented && generation === this.generation) {
          // The access token expired. Renew privately (we hold the lock) and
          // rebuild the body from the renewed pair; the old refresh token is
          // spent now and would only earn a 409.
          await this.performRefresh(generation);
        } else if (status === 409 && generation === this.generation && this.getRefreshToken() !== presented) {
          // Another tab of this remembered session renewed it meanwhile:
          // retry once with the replacement it stored.
        } else {
          throw error;
        }
        presented = this.getRefreshToken();
        response = await send(presented);
      }

      if (generation !== this.generation) {
        // Signed out or signed in elsewhere during the request: never store
        // credentials into a session that is no longer this one.
        throw new SessionChangedError();
      }
      const data = response.data;
      if (data?.token && data.refresh_token) {
        this.setToken(data.token, persist);
        this.setRefreshToken(data.refresh_token, persist);
        return { renewed: true };
      }
      return { renewed: false };
    });
  }

  /**
   * Ends the session: the stored tokens are dropped at once (so no refresh
   * or replay can store or use them any more) and the server is asked to
   * revoke every refresh session of the user. When the access token has
   * expired, it is renewed privately — without storing anything — so the
   * revocation is still authenticated.
   */
  public async endSession(revoke: (accessToken: string) => Promise<void>): Promise<void> {
    const access = this.getToken();
    const refresh = this.getRefreshToken();
    const inFlight = this.refreshInFlight?.promise;
    this.clearTokens();
    if (inFlight) {
      // Let a refresh already on the wire settle first, so the server-side
      // revocation also covers any successor it commits.
      await inFlight.catch(() => undefined);
    }
    if (!access) {
      return;
    }
    try {
      await revoke(access);
    } catch (error) {
      if (statusOf(error) !== 401 || !refresh) {
        throw error;
      }
      const pair = await this.requestRefresh(refresh);
      await revoke(pair.token);
    }
  }

  /**
   * Starts a new session (sign-in, registration): a new generation, and both
   * keys replaced together. A response without a refresh token leaves none,
   * rather than keeping the previous session's.
   */
  public startSession(token: string, refreshToken: string | undefined, persist: boolean): void {
    this.clearTokens();
    this.setToken(token, persist);
    if (refreshToken) {
      this.setRefreshToken(refreshToken, persist);
    }
  }

  public getSessionGeneration(): number {
    return this.generation;
  }

  // Tokens live in localStorage only when the user opted into "remember me";
  // otherwise they go to sessionStorage so the session ends with the browser
  // tab. The session is read from ONE storage: the one holding the access
  // token (sessionStorage first). Its refresh token is never taken from the
  // other storage, so a pair cannot be assembled from two different sessions.
  private sessionStorageArea(): Storage | null {
    if (sessionStorage.getItem(TOKEN_KEY) !== null) return sessionStorage;
    if (localStorage.getItem(TOKEN_KEY) !== null) return localStorage;
    if (sessionStorage.getItem(REFRESH_TOKEN_KEY) !== null) return sessionStorage;
    if (localStorage.getItem(REFRESH_TOKEN_KEY) !== null) return localStorage;
    return null;
  }

  private isPersistent(): boolean {
    return this.sessionStorageArea() === localStorage;
  }

  private writeToken(key: string, token: string, persist: boolean): void {
    const target = persist ? localStorage : sessionStorage;
    const other = persist ? sessionStorage : localStorage;
    other.removeItem(key);
    target.setItem(key, token);
  }

  public getToken(): string | null {
    return this.sessionStorageArea()?.getItem(TOKEN_KEY) ?? null;
  }

  public setToken(token: string, persist = true): void {
    this.writeToken(TOKEN_KEY, token, persist);
  }

  public getRefreshToken(): string | null {
    return this.sessionStorageArea()?.getItem(REFRESH_TOKEN_KEY) ?? null;
  }

  public setRefreshToken(token: string, persist = true): void {
    this.writeToken(REFRESH_TOKEN_KEY, token, persist);
  }

  /** Drops both tokens from both storages and ends the current generation. */
  public clearTokens(): void {
    this.generation += 1;
    for (const key of [TOKEN_KEY, REFRESH_TOKEN_KEY]) {
      localStorage.removeItem(key);
      sessionStorage.removeItem(key);
    }
  }

  public getClient(): AxiosInstance {
    return this.client;
  }
}

export const apiClient = new ApiClient();
export const api = apiClient.getClient();
