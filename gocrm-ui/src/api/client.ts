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
  _session?: SessionSnapshot;
};

/**
 * Which session an operation belongs to, captured when the operation is
 * requested (before it waits for any lock). Another tab of a remembered
 * session writes the same localStorage without changing this tab's
 * generation, so the generation alone cannot tell sessions apart: the account
 * (`subject`) and the refresh token are compared too.
 */
export interface SessionSnapshot {
  readonly generation: number;
  readonly subject: string | null;
  readonly refreshToken: string | null;
}

/** A token pair this tab obtained for a session it may no longer store. */
interface RetainedPair {
  subject: string | null;
  /** The refresh token the pair was rotated from. */
  from: string;
  pair: TokenPair;
}

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

/**
 * The account an access token was issued for (its `user_id` claim), read
 * without verifying the signature — only to tell two sessions apart, never to
 * authorise anything. Null for anything that is not a readable JWT.
 */
export const tokenSubject = (token: string | null): string | null => {
  if (!token) return null;
  const payload = token.split('.')[1];
  if (!payload) return null;
  try {
    const json = atob(payload.replace(/-/g, '+').replace(/_/g, '/'));
    const claims = JSON.parse(json) as { user_id?: unknown };
    return claims.user_id === undefined || claims.user_id === null ? null : String(claims.user_id);
  } catch {
    return null;
  }
};

const statusOf = (error: unknown): number | undefined =>
  (error as AxiosError | undefined)?.response?.status;

/**
 * The HTTP client and the owner of the browser session.
 *
 * Session rules:
 * - Every operation (request, refresh, password change, logout) captures a
 *   SessionSnapshot when it is requested: this tab's generation, the account
 *   of the access token and the refresh token. Before it sends, retries or
 *   writes, storage must still hold that session — same generation, same
 *   account, and the snapshot's refresh token or one this tab rotated from
 *   it. Another tab of a remembered session writes the same localStorage
 *   without changing this tab's generation; the account and the refresh
 *   token lineage catch that, so no operation acts on or writes over a
 *   session other than its own.
 * - Signing in, signing out and an expired session start a new generation; a
 *   refresh or a password change renews the current one.
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
  // Refreshes and password changes still running, so logout can wait for
  // them and revoke whatever successor they commit.
  private sessionOps = new Set<{ generation: number; promise: Promise<unknown> }>();
  // Rotations this tab performed itself (spent refresh token -> successor).
  // A stored refresh token belongs to a snapshot's session only if it is the
  // snapshot's token or descends from it through this map.
  private rotations = new Map<string, string>();
  // Pairs obtained after their session stopped being the stored one. They are
  // never stored; logout uses the one that belongs to the session it ends to
  // authenticate the revocation (the operation spent the stored token).
  private retained: RetainedPair[] = [];
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
        if (sessionConfig._session === undefined) {
          sessionConfig._session = this.captureSession();
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
    const session = config._session ?? this.captureSession();
    if (!this.isCurrentSession(session)) {
      // Sent under a session that has since ended or been replaced (here or
      // by another tab): never refresh or replay it under another one.
      throw new SessionChangedError();
    }
    const sentHeader = config.headers?.Authorization;
    const sentToken = typeof sentHeader === 'string' ? sentHeader.replace(/^Bearer /, '') : undefined;

    const token = await this.refreshFor(session, sentToken);
    if (!this.isCurrentSession(session)) {
      throw new SessionChangedError();
    }
    // Every replay is marked, so a second 401 is final.
    const replay: SessionRequestConfig = { ...config, _retry: true, _session: session };
    replay.headers = config.headers;
    replay.headers.Authorization = `Bearer ${token}`;
    return this.client(replay);
  }

  /** Joins the in-flight refresh of this generation, or starts one. */
  private refreshFor(session: SessionSnapshot, sentToken?: string): Promise<string> {
    if (this.refreshInFlight && this.refreshInFlight.generation === session.generation) {
      return this.refreshInFlight.promise;
    }
    return this.registerRefresh(
      session.generation,
      this.withLock(() => this.performRefresh(session, sentToken))
    );
  }

  /** Makes a refresh joinable by other 401s and visible to logout. */
  private registerRefresh(generation: number, promise: Promise<string>): Promise<string> {
    const entry = { generation, promise };
    this.refreshInFlight = entry;
    this.trackSessionOp(generation, promise);
    promise
      .finally(() => {
        if (this.refreshInFlight === entry) {
          this.refreshInFlight = null;
        }
      })
      .catch(() => undefined);
    return promise;
  }

  private trackSessionOp(generation: number, promise: Promise<unknown>): void {
    const entry = { generation, promise };
    this.sessionOps.add(entry);
    promise
      .finally(() => {
        this.sessionOps.delete(entry);
      })
      .catch(() => undefined);
  }

  /** The session currently stored, for later comparison. */
  public captureSession(): SessionSnapshot {
    return {
      generation: this.generation,
      subject: tokenSubject(this.getToken()),
      refreshToken: this.getRefreshToken(),
    };
  }

  /**
   * Whether storage still holds the session of `snapshot`: same generation,
   * same account, and a refresh token that is the snapshot's or one this tab
   * rotated from it.
   */
  public isCurrentSession(snapshot: SessionSnapshot): boolean {
    return (
      snapshot.generation === this.generation &&
      tokenSubject(this.getToken()) === snapshot.subject &&
      this.descendsFrom(snapshot.refreshToken, this.getRefreshToken())
    );
  }

  private descendsFrom(from: string | null, token: string | null): boolean {
    if (from === null) {
      return token === null;
    }
    const seen = new Set<string>();
    let current: string | undefined = from;
    while (current !== undefined && !seen.has(current)) {
      if (current === token) {
        return true;
      }
      seen.add(current);
      current = this.rotations.get(current);
    }
    return false;
  }

  private recordRotation(from: string, to: string): void {
    this.rotations.set(from, to);
    if (this.rotations.size > 100) {
      const oldest = this.rotations.keys().next().value;
      if (oldest !== undefined) this.rotations.delete(oldest);
    }
  }

  private retain(subject: string | null, from: string, pair: TokenPair): void {
    this.retained.push({ subject, from, pair });
    if (this.retained.length > 10) {
      this.retained.shift();
    }
  }

  /** The most recent retained pair that belongs to the session `ending`. */
  private takeRetained(ending: SessionSnapshot): TokenPair | null {
    for (let i = this.retained.length - 1; i >= 0; i--) {
      const candidate = this.retained[i];
      if (candidate.subject === ending.subject && this.descendsFrom(ending.refreshToken, candidate.from)) {
        this.retained.splice(i, 1);
        return candidate.pair;
      }
    }
    return null;
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
  private async performRefresh(session: SessionSnapshot, sentToken?: string): Promise<string> {
    if (!this.isCurrentSession(session)) {
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
      this.expireSession(session);
      throw new Error('No refresh token available');
    }
    const persist = this.isPersistent();

    let pair: TokenPair;
    try {
      pair = await this.requestRefresh(presented);
    } catch (error) {
      if (!this.isCurrentSession(session)) {
        throw new SessionChangedError();
      }
      const status = statusOf(error);
      if (status === 400 || status === 401) {
        this.expireSession(session);
      }
      throw error;
    }

    // The session ended or was replaced (here or by another tab) while the
    // refresh was in flight: the late pair must not resurrect or overwrite
    // anything. It is kept privately so a logout of this session can still
    // authenticate its revocation.
    if (!this.isCurrentSession(session) || this.getRefreshToken() !== presented) {
      this.retain(session.subject, presented, pair);
      throw new SessionChangedError();
    }
    this.setToken(pair.token, persist);
    this.setRefreshToken(pair.refresh_token, persist);
    this.recordRotation(presented, pair.refresh_token);
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

  private expireSession(session: SessionSnapshot): void {
    // Never clear a session that is not the one that failed: in shared
    // storage that could be another tab's sign-in.
    if (!this.isCurrentSession(session)) {
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
    // The session this change is for, captured before waiting for the lock.
    // Every attempt, retry and write below is checked against it.
    const session = this.captureSession();
    const operation = this.withLock(async () => {
      if (!this.isCurrentSession(session)) {
        throw new SessionChangedError();
      }
      const persist = this.isPersistent();
      // Every attempt is authenticated with the pair read for it, so the
      // request interceptor cannot substitute whatever another tab stored.
      const send = (accessToken: string | null, refreshToken: string | null) =>
        this.client.post<ChangePasswordResponse>(
          '/auth/change-password',
          {
            current_password: currentPassword,
            new_password: newPassword,
            ...(refreshToken ? { refresh_token: refreshToken } : {}),
          },
          {
            _session: session,
            ...(accessToken ? { headers: { Authorization: `Bearer ${accessToken}` } } : {}),
          } as Partial<SessionRequestConfig>
        );

      let presented = this.getRefreshToken();
      let response: AxiosResponse<ChangePasswordResponse>;
      try {
        response = await send(this.getToken(), presented);
      } catch (error) {
        // Only an expired access token of this same session is recovered: it
        // is renewed (as a registered refresh, so logout waits for it) and the
        // body rebuilt from the renewed pair, the old refresh token being
        // spent. A 409 SESSION_STALE always surfaces — following a rotation
        // made by another tab needs cross-tab coordination (#184).
        if (statusOf(error) !== 401 || !presented || !this.isCurrentSession(session)) {
          throw error;
        }
        await this.registerRefresh(session.generation, this.performRefresh(session));
        if (!this.isCurrentSession(session)) {
          throw new SessionChangedError();
        }
        presented = this.getRefreshToken();
        response = await send(this.getToken(), presented);
      }

      const data = response.data;
      if (data?.token && data.refresh_token) {
        const pair = { token: data.token, refresh_token: data.refresh_token };
        if (!this.isCurrentSession(session)) {
          // Signed out, or another account signed in (here or in another
          // tab), while the request was in flight: never write over that.
          if (presented) this.retain(session.subject, presented, pair);
          throw new SessionChangedError();
        }
        this.setToken(pair.token, persist);
        this.setRefreshToken(pair.refresh_token, persist);
        if (presented) this.recordRotation(presented, pair.refresh_token);
        return { renewed: true };
      }
      return { renewed: false };
    });
    this.trackSessionOp(session.generation, operation);
    return operation;
  }

  /**
   * Ends the session: the stored tokens are dropped at once (so no refresh
   * or replay can store or use them any more) and the server is asked to
   * revoke every refresh session of the user. When the access token has
   * expired, it is renewed privately — without storing anything — so the
   * revocation is still authenticated.
   */
  public async endSession(revoke: (accessToken: string) => Promise<void>): Promise<void> {
    const ending = this.captureSession();
    const access = this.getToken();
    const running = [...this.sessionOps].filter((op) => op.generation === ending.generation).map((op) => op.promise);
    this.clearTokens();
    // Let refreshes and password changes already running settle first, so
    // the server-side revocation also covers any successor they commit.
    await Promise.allSettled(running);
    // Such an operation spent the stored refresh token; its successor (kept
    // privately, never stored) is the freshest credential of the ending
    // session — but only one that belongs to this session (same account,
    // rotated from this session's refresh token) may be used.
    const successor = this.takeRetained(ending);
    const accessToken = successor?.token ?? access;
    const refreshToken = successor?.refresh_token ?? ending.refreshToken;
    if (!accessToken) {
      return;
    }
    try {
      await revoke(accessToken);
    } catch (error) {
      if (statusOf(error) !== 401 || !refreshToken) {
        throw error;
      }
      const pair = await this.requestRefresh(refreshToken);
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
