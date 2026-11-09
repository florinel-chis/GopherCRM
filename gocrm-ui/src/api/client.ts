import axios, { AxiosError } from 'axios';
import type { AxiosAdapter, AxiosInstance, AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import type { APIError, ChangePasswordResponse } from '@/types';
import { createSessionLock, type SessionLock } from './sessionLock';
import {
  REFRESH_TOKEN_KEY,
  SESSION_KEY,
  TOKEN_KEY,
  legacySessionId,
  newSessionId,
  parseSessionRecord,
  serializeSessionRecord,
  type SessionRecord,
} from './sessionRecord';

export { REFRESH_TOKEN_KEY, SESSION_KEY, TOKEN_KEY };
export type { SessionRecord };

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

// A refresh that has not answered within this time — counted from the moment
// it was requested, so the wait for the session lock is included — settles
// every request waiting on it. The deadline rejects on its own: aborting the
// lock request and the HTTP call is only cleanup, because an adapter that
// ignores the abort would otherwise keep every waiter pending forever.
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
 * requested (before it waits for any lock): this tab's generation and the
 * stored record's session id, plus the account and refresh token read with it.
 * A session keeps its id across refreshes and password changes — in every
 * tab — and a sign-in always gets a new one, so the id tells sessions apart
 * even when another tab rewrites the shared storage.
 */
export interface SessionSnapshot {
  readonly generation: number;
  readonly id: string | null;
  readonly subject: string | null;
  readonly refreshToken: string | null;
}

/** A token pair this tab obtained for a session it may no longer store. */
interface RetainedPair {
  id: string;
  pair: TokenPair;
}

interface TokenPair {
  token: string;
  refresh_token: string;
}

/** The record currently stored, and the storage that holds it. */
interface StoredSession {
  record: SessionRecord;
  storage: Storage;
}

interface Deadline {
  signal: AbortSignal;
  expired: Promise<never>;
  cancel: () => void;
}

export interface ApiClientOptions {
  baseURL?: string;
  timeout?: number;
  /** Transport for both the API and the refresh calls (tests inject one). */
  adapter?: AxiosAdapter;
  refreshTimeoutMs?: number;
  /** Called once when a session ends because it can no longer be renewed. */
  onSessionExpired?: () => void;
  /**
   * The origin-wide lock refreshes and password changes run under. Defaults
   * to the Web Lock `gophercrm-session` (in-tab fallback without
   * `navigator.locks`); tests share one memory lock between clients.
   */
  sessionLock?: SessionLock;
  /** The browser storages (tests inject instrumented ones). */
  storage?: { local: Storage; session: Storage };
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
 * - The session is one record (`gophercrm_session`: id, access token, refresh
 *   token, account, persistence) read whole from one storage, sessionStorage
 *   first. The legacy keys are write-only mirrors; a record whose
 *   `gophercrm_token` mirror is gone is no session.
 * - Every operation (request, refresh, password change, logout) captures a
 *   SessionSnapshot when it is requested. Before it sends, retries or writes,
 *   storage must still hold that session: same generation in this tab and
 *   the same record id. Another tab's refresh keeps the id (the operation
 *   follows it); another tab's sign-in or sign-out changes or removes it (the
 *   operation stops with SessionChangedError).
 * - Signing in, signing out and an expired session start a new generation; a
 *   refresh or a password change renews the current one.
 * - Refreshes and password changes run under one origin-wide exclusive lock
 *   (Web Locks `gophercrm-session`) and re-read the record once they hold it:
 *   a successor another tab already stored is adopted instead of spending the
 *   refresh token again. Sign-in and sign-out never wait for the lock.
 * - In one tab, every request that hits a 401 waits on the same refresh.
 * - A refresh rejected with 400/401 ends the session only while storage still
 *   holds the refresh token that was presented; if another tab stored a
 *   successor meanwhile it is adopted. A timeout, network failure or 5xx keeps
 *   the stored tokens.
 */
export class ApiClient {
  private client: AxiosInstance;
  private sessionHttp: AxiosInstance;
  private refreshTimeoutMs: number;
  private onSessionExpired: () => void;
  private lock: SessionLock;
  private storages?: { local: Storage; session: Storage };
  private generation = 0;
  private refreshInFlight: { generation: number; id: string | null; promise: Promise<string> } | null = null;
  // Refreshes and password changes still running, so logout can wait for
  // them and revoke whatever successor they commit.
  private sessionOps = new Set<{ generation: number; promise: Promise<unknown> }>();
  // Pairs obtained after their session stopped being the stored one. They are
  // never stored; logout uses the one that belongs to the session it ends to
  // authenticate the revocation (the operation spent the stored token).
  private retained: RetainedPair[] = [];

  constructor(options: ApiClientOptions = {}) {
    const baseURL = options.baseURL ?? API_BASE_URL;
    this.refreshTimeoutMs = options.refreshTimeoutMs ?? REFRESH_TIMEOUT_MS;
    this.onSessionExpired = options.onSessionExpired ?? defaultSessionExpired;
    this.lock = options.sessionLock ?? createSessionLock();
    this.storages = options.storage;
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

  /** Joins this tab's in-flight refresh of the same session, or starts one. */
  private refreshFor(session: SessionSnapshot, sentToken?: string): Promise<string> {
    const inFlight = this.refreshInFlight;
    if (inFlight && inFlight.generation === session.generation && inFlight.id === session.id) {
      return inFlight.promise;
    }
    return this.registerRefresh(session, this.refreshUnderLock(session, sentToken));
  }

  /** Makes a refresh joinable by other 401s and visible to logout. */
  private registerRefresh(session: SessionSnapshot, promise: Promise<string>): Promise<string> {
    const entry = { generation: session.generation, id: session.id, promise };
    this.refreshInFlight = entry;
    this.trackSessionOp(session.generation, promise);
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
    const stored = this.readStored();
    return {
      generation: this.generation,
      id: stored?.record.id ?? null,
      subject: stored?.record.subject ?? null,
      refreshToken: stored?.record.refresh ?? null,
    };
  }

  /**
   * Whether storage still holds the session of `snapshot`: same generation in
   * this tab and the same record id (null for no session).
   */
  public isCurrentSession(snapshot: SessionSnapshot): boolean {
    return this.matches(snapshot, this.readStored());
  }

  private matches(snapshot: SessionSnapshot, stored: StoredSession | null): boolean {
    return snapshot.generation === this.generation && (stored?.record.id ?? null) === snapshot.id;
  }

  private retain(id: string | null, pair: TokenPair): void {
    if (id === null) return;
    this.retained.push({ id, pair });
    if (this.retained.length > 10) {
      this.retained.shift();
    }
  }

  /** The most recent retained pair that belongs to the session `ending`. */
  private takeRetained(ending: SessionSnapshot): TokenPair | null {
    for (let i = this.retained.length - 1; i >= 0; i--) {
      const candidate = this.retained[i];
      if (ending.id !== null && candidate.id === ending.id) {
        this.retained.splice(i, 1);
        return candidate.pair;
      }
    }
    return null;
  }

  /**
   * One refresh under the session lock, bounded by the refresh deadline from
   * the moment it is requested. A refresh still queued for the lock at the
   * deadline is withdrawn and never runs.
   */
  private async refreshUnderLock(session: SessionSnapshot, sentToken?: string): Promise<string> {
    const deadline = this.startDeadline();
    try {
      return await Promise.race([
        this.lock.run(() => this.performRefresh(session, sentToken, deadline), deadline.signal),
        deadline.expired,
      ]);
    } finally {
      deadline.cancel();
    }
  }

  private startDeadline(): Deadline {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const expired = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        const error = new RefreshTimeoutError();
        reject(error);
        controller.abort(error);
      }, this.refreshTimeoutMs);
    });
    expired.catch(() => undefined);
    return { signal: controller.signal, expired, cancel: () => clearTimeout(timer) };
  }

  /**
   * Renews the session of `session`. Must only run while holding the lock; it
   * never takes the lock itself, so a lock holder (the password change) can
   * call it without acquiring the lock a second time.
   *
   * `sentToken` is the access token the failed request carried: if the
   * record now holds another one, a refresh (in this tab or another) already
   * renewed the session and its access token is adopted without spending the
   * refresh token again.
   */
  private async performRefresh(session: SessionSnapshot, sentToken?: string, deadline?: Deadline): Promise<string> {
    const stored = this.readStored();
    if (!this.matches(session, stored)) {
      throw new SessionChangedError();
    }
    if (!stored) {
      // No session to renew (the snapshot was taken without one).
      this.expireSession(session);
      throw new Error('No refresh token available');
    }
    if (sentToken && stored.record.access !== sentToken) {
      return stored.record.access;
    }

    const presented = stored.record.refresh;
    if (!presented) {
      this.expireSession(session);
      throw new Error('No refresh token available');
    }

    let pair: TokenPair;
    try {
      pair = await this.requestRefresh(presented, deadline);
    } catch (error) {
      const status = statusOf(error);
      if (status === 400 || status === 401) {
        return this.recoverRejectedRefresh(session, presented, error);
      }
      if (!this.isCurrentSession(session)) {
        throw new SessionChangedError();
      }
      throw error;
    }

    const now = this.readStored();
    if (!this.matches(session, now) || !now) {
      // The session ended or was replaced (here or by another tab) while the
      // refresh was in flight: the late pair must not resurrect or overwrite
      // anything. It is kept privately so a logout of this session can still
      // authenticate its revocation.
      this.retain(session.id, pair);
      throw new SessionChangedError();
    }
    if (now.record.refresh !== presented) {
      // Another writer of this session (a tab without a shared lock) stored a
      // newer pair meanwhile; keep it and follow it.
      this.retain(session.id, pair);
      return now.record.access;
    }
    this.writeRecord(now.storage, { ...now.record, access: pair.token, refresh: pair.refresh_token });
    return pair.token;
  }

  /**
   * A refresh answered 400/401. That ends the session only while storage still
   * holds the token that was presented: a tab whose locks are not shared (no
   * Web Locks) may have spent it first and stored the successor, which is then
   * adopted. A record of another session (or none) is left alone.
   */
  private recoverRejectedRefresh(session: SessionSnapshot, presented: string, error: unknown): string {
    const now = this.readStored();
    if (!this.matches(session, now) || !now) {
      throw new SessionChangedError();
    }
    if (now.record.refresh !== presented) {
      return now.record.access;
    }
    this.expireSession(session);
    throw error;
  }

  /** POST /auth/refresh under the refresh deadline; writes nothing. */
  private async requestRefresh(refreshToken: string, deadline?: Deadline): Promise<TokenPair> {
    const call = (signal: AbortSignal) =>
      this.sessionHttp.post('/auth/refresh', { refresh_token: refreshToken }, { signal });
    const response = deadline
      ? await Promise.race([call(deadline.signal), deadline.expired])
      : await this.withDeadline(call);
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
    const deadline = this.startDeadline();
    try {
      return await Promise.race([call(deadline.signal), deadline.expired]);
    } finally {
      deadline.cancel();
    }
  }

  private expireSession(session: SessionSnapshot): void {
    // Never clear a session that is not the one that failed: in shared
    // storage that could be another tab's sign-in.
    if (!this.isCurrentSession(session)) {
      return;
    }
    // A snapshot without a session has nothing to remove: a record another
    // tab is writing right now must survive.
    if (session.id !== null) {
      this.clearTokens();
    }
    this.onSessionExpired();
  }

  /**
   * Changes the password and keeps this session renewable. Runs under the
   * session lock and reads the record once it holds it, so the request
   * carries the pair another tab may have rotated to meanwhile; the
   * replacement pair the server returns is written over the same record
   * (same id, same storage). An expired access token is renewed inside the
   * same lock hold and the request rebuilt from the renewed pair.
   */
  public async changePassword(currentPassword: string, newPassword: string): Promise<{ renewed: boolean }> {
    // The session this change is for, captured before waiting for the lock.
    // Every attempt, retry and write below is checked against it.
    const session = this.captureSession();
    const operation = this.lock.run(async () => {
      const start = this.readStored();
      if (!this.matches(session, start)) {
        throw new SessionChangedError();
      }
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

      let access = start?.record.access ?? null;
      let presented = start?.record.refresh ?? null;
      let response: AxiosResponse<ChangePasswordResponse>;
      try {
        response = await send(access, presented);
      } catch (error) {
        // Only an expired access token of this same session is recovered: it
        // is renewed (as a registered refresh, so logout waits for it) and the
        // body rebuilt from the renewed pair, the old refresh token being
        // spent. A 409 SESSION_STALE always surfaces.
        if (statusOf(error) !== 401 || !presented || !this.isCurrentSession(session)) {
          throw error;
        }
        await this.registerRefresh(session, this.performRefresh(session, access ?? undefined));
        const renewed = this.readStored();
        if (!this.matches(session, renewed) || !renewed) {
          throw new SessionChangedError();
        }
        access = renewed.record.access;
        presented = renewed.record.refresh;
        response = await send(access, presented);
      }

      const data = response.data;
      if (data?.token && data.refresh_token) {
        const pair = { token: data.token, refresh_token: data.refresh_token };
        const now = this.readStored();
        if (!this.matches(session, now) || !now) {
          // Signed out, or another account signed in (here or in another
          // tab), while the request was in flight: never write over that.
          this.retain(session.id, pair);
          throw new SessionChangedError();
        }
        this.writeRecord(now.storage, { ...now.record, access: pair.token, refresh: pair.refresh_token });
        return { renewed: true };
      }
      return { renewed: false };
    });
    this.trackSessionOp(session.generation, operation);
    return operation;
  }

  /**
   * Ends the session: the stored record is dropped at once — without waiting
   * for the session lock, so another tab's refresh cannot delay a sign-out —
   * and the server is asked to revoke every refresh session of the user. When
   * the access token has expired, it is renewed privately — without storing
   * anything — so the revocation is still authenticated.
   */
  public async endSession(revoke: (accessToken: string) => Promise<void>): Promise<void> {
    const stored = this.readStored();
    const ending = this.captureSession();
    const access = stored?.record.access ?? null;
    const running = [...this.sessionOps].filter((op) => op.generation === ending.generation).map((op) => op.promise);
    this.clearTokens();
    // Let refreshes and password changes of this tab already running settle
    // first, so the server-side revocation also covers any successor they
    // commit.
    await Promise.allSettled(running);
    // Such an operation spent the stored refresh token; its successor (kept
    // privately, never stored) is the freshest credential of the ending
    // session — but only one obtained for this very session may be used.
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
   * Starts a new session (sign-in, registration): a new generation and a new
   * record with a fresh random id, in localStorage when `persist` is true and
   * in sessionStorage otherwise. Anything stored before, in either storage,
   * is removed first. A response without a refresh token stores none.
   */
  public startSession(token: string, refreshToken: string | undefined, persist: boolean): void {
    this.clearTokens();
    this.writeRecord(persist ? this.localArea() : this.sessionArea(), {
      v: 1,
      id: newSessionId(),
      access: token,
      refresh: refreshToken ?? null,
      subject: tokenSubject(token),
      persist,
    });
  }

  public getSessionGeneration(): number {
    return this.generation;
  }

  public getToken(): string | null {
    return this.readStored()?.record.access ?? null;
  }

  public getRefreshToken(): string | null {
    return this.readStored()?.record.refresh ?? null;
  }

  /** Drops the record and the legacy keys from both storages and ends the current generation. */
  public clearTokens(): void {
    this.generation += 1;
    for (const storage of [this.sessionArea(), this.localArea()]) {
      storage.removeItem(SESSION_KEY);
      storage.removeItem(TOKEN_KEY);
      storage.removeItem(REFRESH_TOKEN_KEY);
    }
  }

  public getClient(): AxiosInstance {
    return this.client;
  }

  private localArea(): Storage {
    return this.storages?.local ?? localStorage;
  }

  private sessionArea(): Storage {
    return this.storages?.session ?? sessionStorage;
  }

  /**
   * Writes the whole record in one setItem, then the legacy mirrors of the
   * same storage (always after the record).
   */
  private writeRecord(storage: Storage, record: SessionRecord): void {
    storage.setItem(SESSION_KEY, serializeSessionRecord(record));
    storage.setItem(TOKEN_KEY, record.access);
    if (record.refresh !== null) {
      storage.setItem(REFRESH_TOKEN_KEY, record.refresh);
    } else {
      storage.removeItem(REFRESH_TOKEN_KEY);
    }
  }

  /**
   * The stored session, read whole from one storage (sessionStorage first).
   * A malformed record is removed with its mirrors. A record without its
   * `gophercrm_token` mirror is no session but stays where it is (it may be
   * mid-write by another tab, or signed out by a browser spec). Legacy keys
   * are migrated only when neither storage holds any record.
   */
  private readStored(): StoredSession | null {
    let sawRecord = false;
    for (const storage of [this.sessionArea(), this.localArea()]) {
      const raw = storage.getItem(SESSION_KEY);
      if (raw === null) continue;
      sawRecord = true;
      const record = parseSessionRecord(raw);
      if (!record) {
        storage.removeItem(SESSION_KEY);
        storage.removeItem(TOKEN_KEY);
        storage.removeItem(REFRESH_TOKEN_KEY);
        continue;
      }
      if (storage.getItem(TOKEN_KEY) === null) {
        return null;
      }
      return { record, storage };
    }
    return sawRecord ? null : this.migrateLegacy();
  }

  /**
   * Turns a session stored under the two legacy keys into a record, once.
   * The storage holding `gophercrm_token` (sessionStorage first) is taken
   * with its own refresh token or none; a refresh token without an access
   * token beside it is dropped.
   */
  private migrateLegacy(): StoredSession | null {
    const areas = [this.sessionArea(), this.localArea()];
    for (const storage of areas) {
      if (storage.getItem(TOKEN_KEY) === null && storage.getItem(REFRESH_TOKEN_KEY) !== null) {
        storage.removeItem(REFRESH_TOKEN_KEY);
      }
    }
    for (const storage of areas) {
      const access = storage.getItem(TOKEN_KEY);
      if (access === null) continue;
      const refresh = storage.getItem(REFRESH_TOKEN_KEY);
      const record: SessionRecord = {
        v: 1,
        id: legacySessionId(access, refresh),
        access,
        refresh,
        subject: tokenSubject(access),
        persist: storage === this.localArea(),
      };
      this.writeRecord(storage, record);
      return { record, storage };
    }
    return null;
  }
}

export const apiClient = new ApiClient();
export const api = apiClient.getClient();
