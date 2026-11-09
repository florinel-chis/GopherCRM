import { describe, it, expect, vi, afterEach } from 'vitest';
import type { AxiosResponse, InternalAxiosRequestConfig } from 'axios';
import { ApiClient } from './client';
import { SESSION_LOCK_NAME, createMemorySessionLock, createSessionLock } from './sessionLock';

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

const flush = async () => {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve();
  }
};

const setLocks = (value: unknown) => {
  Object.defineProperty(navigator, 'locks', { value, configurable: true, writable: true });
};

describe('createSessionLock', () => {
  afterEach(() => {
    Reflect.deleteProperty(navigator, 'locks');
  });

  it('uses an exclusive navigator.locks request named gophercrm-session when the browser provides one', async () => {
    const request = vi.fn(
      (_name: string, _options: { mode: string; signal?: AbortSignal }, callback: () => Promise<unknown>) =>
        Promise.resolve().then(callback)
    );
    setLocks({ request });

    const lock = createSessionLock();
    const controller = new AbortController();
    await expect(lock.run(async () => 42, controller.signal)).resolves.toBe(42);

    expect(SESSION_LOCK_NAME).toBe('gophercrm-session');
    expect(request).toHaveBeenCalledTimes(1);
    const [name, options, callback] = request.mock.calls[0];
    expect(name).toBe('gophercrm-session');
    expect(options.mode).toBe('exclusive');
    expect(options.signal).toBe(controller.signal);
    expect(typeof callback).toBe('function');

    // The holder's rejection is the lock's rejection.
    const failure = new Error('holder failed');
    await expect(
      lock.run(async () => {
        throw failure;
      })
    ).rejects.toBe(failure);
    expect(request).toHaveBeenCalledTimes(2);
    expect(request.mock.calls[1][1].mode).toBe('exclusive');
  });

  it('falls back to an in-tab lock when navigator.locks is unavailable', async () => {
    setLocks(undefined);
    const lock = createSessionLock();

    const first = deferred<string>();
    const order: string[] = [];
    let running = 0;
    let maxRunning = 0;
    const holder = (label: string, work: () => Promise<string>) => async () => {
      running += 1;
      maxRunning = Math.max(maxRunning, running);
      order.push(`start ${label}`);
      try {
        return await work();
      } finally {
        order.push(`end ${label}`);
        running -= 1;
      }
    };

    const a = lock.run(holder('a', () => first.promise));
    const b = lock.run(
      holder('b', async () => {
        throw new Error('b failed');
      })
    );
    const c = lock.run(holder('c', async () => 'c'));
    await flush();
    expect(order).toEqual(['start a']);

    first.resolve('a');
    await expect(a).resolves.toBe('a');
    // A rejected holder does not break the chain.
    await expect(b).rejects.toThrow('b failed');
    await expect(c).resolves.toBe('c');
    expect(order).toEqual(['start a', 'end a', 'start b', 'end b', 'start c', 'end c']);
    expect(maxRunning).toBe(1);
  });
});

describe('createMemorySessionLock', () => {
  it('runs one holder at a time across clients that share it', async () => {
    const lock = createMemorySessionLock();
    expect(lock.acquisitions).toBe(0);
    expect(() => {
      (lock as unknown as { acquisitions: number }).acquisitions = 99;
    }).toThrow(TypeError);

    // Two tabs: each password change is a lock holder.
    const answers: Array<() => void> = [];
    const sent: string[] = [];
    const tab = (name: string) =>
      new ApiClient({
        baseURL: 'http://api.test',
        sessionLock: lock,
        onSessionExpired: vi.fn(),
        adapter: (config: InternalAxiosRequestConfig) => {
          sent.push(`${name} ${config.url}`);
          return new Promise<AxiosResponse>((resolve) => {
            answers.push(() =>
              resolve({
                data: { success: true, data: { message: 'Password changed successfully' } },
                status: 200,
                statusText: '200',
                headers: {},
                config,
              })
            );
          });
        },
      });
    const tabA = tab('A');
    const tabB = tab('B');
    tabA.startSession('access', 'r0', true);

    const changeA = tabA.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    const changeB = tabB.changePassword('OldStrongPass1!', 'NewStrongPass1!');
    await vi.waitFor(() => expect(sent).toEqual(['A /auth/change-password']));
    await flush();
    expect(sent).toEqual(['A /auth/change-password']);
    expect(lock.acquisitions).toBe(1);

    answers[0]();
    await expect(changeA).resolves.toEqual({ renewed: false });
    await vi.waitFor(() => expect(sent).toEqual(['A /auth/change-password', 'B /auth/change-password']));
    expect(lock.acquisitions).toBe(2);
    answers[1]();
    await expect(changeB).resolves.toEqual({ renewed: false });

    // Plain holders: never two at once.
    let running = 0;
    let maxRunning = 0;
    const work = async () => {
      running += 1;
      maxRunning = Math.max(maxRunning, running);
      await flush();
      running -= 1;
    };
    await Promise.all([lock.run(work), lock.run(work), lock.run(work)]);
    expect(maxRunning).toBe(1);
    expect(lock.acquisitions).toBe(5);
  });

  it('never runs a waiter whose signal aborted while it queued', async () => {
    const lock = createMemorySessionLock();
    const blocker = deferred<void>();
    const ran: string[] = [];

    const first = lock.run(async () => {
      ran.push('first');
      await blocker.promise;
    });
    const controller = new AbortController();
    const aborted = lock.run(async () => {
      ran.push('aborted');
    }, controller.signal);
    const later = lock.run(async () => {
      ran.push('later');
      return 'later';
    });
    await flush();

    const reason = new Error('gave up waiting');
    controller.abort(reason);
    await expect(aborted).rejects.toBe(reason);

    blocker.resolve();
    await first;
    await expect(later).resolves.toBe('later');
    expect(ran).toEqual(['first', 'later']);
    expect(lock.acquisitions).toBe(2);

    // A signal that is already aborted never queues.
    const gone = new AbortController();
    gone.abort(reason);
    await expect(lock.run(async () => ran.push('never'), gone.signal)).rejects.toBe(reason);
    expect(ran).toEqual(['first', 'later']);
    expect(lock.acquisitions).toBe(2);
  });
});
