/**
 * The origin-wide lock that serialises session renewals (#184).
 *
 * Every refresh and every password change of the web client runs as a holder
 * of this lock. With the Web Locks API (secure contexts) the lock is shared by
 * every tab of the origin, so two tabs of one remembered session never present
 * the same refresh token at once. Where `navigator.locks` is missing (a plain
 * HTTP deployment such as the bundled nginx) the fallback is an in-tab queue:
 * tabs are then not coordinated by the lock, and the client relies on
 * re-reading the stored session record instead (see client.ts).
 */

export const SESSION_LOCK_NAME = 'gophercrm-session';

export interface SessionLock {
  /**
   * Runs `holder` once the lock is granted and settles with its result. A
   * `signal` that aborts while the holder still waits rejects with the abort
   * reason and the holder never runs.
   */
  run<T>(holder: () => Promise<T>, signal?: AbortSignal): Promise<T>;
}

export interface MemorySessionLock extends SessionLock {
  /** How many holders were granted the lock (aborted waiters are not counted). */
  readonly acquisitions: number;
}

const abortReason = (signal: AbortSignal): unknown =>
  signal.reason ?? new DOMException('The operation was aborted', 'AbortError');

interface Waiter {
  grant: () => void;
}

class InTabSessionLock implements MemorySessionLock {
  private held = false;
  private queue: Waiter[] = [];
  private granted = 0;

  get acquisitions(): number {
    return this.granted;
  }

  run<T>(holder: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      if (signal?.aborted) {
        reject(abortReason(signal));
        return;
      }
      const onAbort = () => {
        const index = this.queue.indexOf(waiter);
        if (index >= 0) {
          this.queue.splice(index, 1);
          reject(signal ? abortReason(signal) : new DOMException('Aborted', 'AbortError'));
        }
      };
      const waiter: Waiter = {
        grant: () => {
          signal?.removeEventListener('abort', onAbort);
          this.granted += 1;
          let result: Promise<T>;
          try {
            result = Promise.resolve(holder());
          } catch (error) {
            result = Promise.reject(error);
          }
          // A rejected holder releases the lock like a resolved one, so the
          // queue never stalls.
          result.then(resolve, reject).finally(() => this.release());
        },
      };
      if (!this.held) {
        this.held = true;
        waiter.grant();
        return;
      }
      this.queue.push(waiter);
      signal?.addEventListener('abort', onAbort, { once: true });
    });
  }

  private release(): void {
    const next = this.queue.shift();
    if (next) {
      next.grant();
    } else {
      this.held = false;
    }
  }
}

/**
 * An in-memory lock: holders run one at a time, in request order. One
 * instance passed to several ApiClients (tests) stands for the origin-wide
 * Web Lock; it is also the fallback where `navigator.locks` is missing.
 */
export function createMemorySessionLock(): MemorySessionLock {
  return new InTabSessionLock();
}

interface LockRequester {
  request(
    name: string,
    options: { mode: 'exclusive'; signal?: AbortSignal },
    callback: () => Promise<unknown>
  ): Promise<unknown>;
}

const webLocks = (): LockRequester | undefined => {
  if (typeof navigator === 'undefined') return undefined;
  const locks = (navigator as unknown as { locks?: LockRequester }).locks;
  return locks && typeof locks.request === 'function' ? locks : undefined;
};

/**
 * The browser's session lock: the exclusive Web Lock `gophercrm-session` when
 * `navigator.locks` exists, otherwise an in-tab promise-chain lock.
 */
export function createSessionLock(): SessionLock {
  const locks = webLocks();
  if (!locks) {
    return createMemorySessionLock();
  }
  return {
    run<T>(holder: () => Promise<T>, signal?: AbortSignal): Promise<T> {
      const options: { mode: 'exclusive'; signal?: AbortSignal } = { mode: 'exclusive' };
      if (signal) options.signal = signal;
      return locks.request(SESSION_LOCK_NAME, options, () => holder()) as Promise<T>;
    },
  };
}
