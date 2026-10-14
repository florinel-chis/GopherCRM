import { test as base, expect, type APIResponse } from '@playwright/test';
import type { AEOProfile, AEOProviderStatus, AEORun } from '../../src/api/endpoints/aeo';
import type { Configuration } from '../../src/api/endpoints/configurations';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { AEOProviderStub } from '../helpers/aeo-provider-stub';
import { API_BASE_URL } from '../helpers/env';

interface AEOFixture {
  stub: AEOProviderStub;
  token: string;
  originalProviders: AEOProviderStatus[];
  response(method: string, path: string, data?: unknown, token?: string): Promise<APIResponse>;
  api<T>(method: string, path: string, data?: unknown, status?: number): Promise<T>;
  configurations(): Promise<Configuration[]>;
  createPrompt(text: string): Promise<number>;
  waitForRun(id: number, status?: 'completed' | 'failed'): Promise<void>;
}

export const test = base.extend<{ aeo: AEOFixture }>({
  aeo: async ({ page, request }, provideFixture) => {
    // Config saves invalidate several queries. Pace real browser and fixture
    // traffic below the protected API's 2 requests/sec limit, including reloads.
    let nextRequestAt = Date.now();
    const gate = async () => {
      const scheduledAt = nextRequestAt;
      nextRequestAt = Math.max(Date.now(), scheduledAt) + 550;
      await new Promise((resolve) => setTimeout(resolve, Math.max(0, scheduledAt - Date.now())));
    };
    await page.route(`${API_BASE_URL}/**`, async (route) => {
      if (!new URL(route.request().url()).pathname.includes('/auth/')) await gate();
      await route.continue();
    });
    await new AdminAuthHelper(page).createAndLoginAdmin();
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    if (!token) throw new Error('Admin login did not store its token');

    const response = async (method: string, path: string, data?: unknown, bearer = token) => {
      await gate();
      return request.fetch(`${API_BASE_URL}${path}`, {
        method,
        headers: { Authorization: `Bearer ${bearer}` },
        data,
      });
    };
    const api = async <T>(
      method: string,
      path: string,
      data?: unknown,
      status = 200,
    ): Promise<T> => {
      const result = await response(method, path, data);
      expect(result.status(), `${method} ${path}: ${await result.text()}`).toBe(status);
      return status === 204 ? (undefined as T) : ((await result.json()).data as T);
    };

    const configurations = async () =>
      (
        await api<{ configurations: Configuration[] }>(
          'GET',
          '/configurations/category/integration',
        )
      ).configurations;
    const originals = await configurations();
    const originalProviders = await api<AEOProviderStatus[]>('GET', '/aeo/providers');
    expect(
      originalProviders.filter((provider) => provider.configured),
      'AEO provider tests require the fresh database and empty keys supplied by make e2e',
    ).toEqual([]);
    expect(originals.filter((config) => config.is_sensitive && config.is_set)).toEqual([]);

    const profileResponse = await response('GET', '/aeo/profile');
    expect([200, 404]).toContain(profileResponse.status());
    const originalProfile: AEOProfile | null = profileResponse.ok()
      ? (await profileResponse.json()).data
      : null;
    const promptIds: number[] = [];
    const runIds = new Set<number>();
    const runResponses: Promise<void>[] = [];
    page.on('response', (result) => {
      if (
        result.status() === 202 &&
        /\/aeo\/(runs|prompts\/\d+\/run)$/.test(new URL(result.url()).pathname)
      ) {
        runResponses.push(
          result.json().then(({ data }: { data: AEORun }) => {
            runIds.add(data.id);
          }),
        );
      }
    });
    const stub = new AEOProviderStub();
    await stub.start();
    try {
      await provideFixture({
        stub,
        token,
        originalProviders,
        response,
        api,
        configurations,
        createPrompt: async (text) => {
          const prompts = await api<{ id: number }[]>(
            'POST',
            '/aeo/prompts',
            { prompts: [text] },
            201,
          );
          promptIds.push(prompts[0].id);
          return prompts[0].id;
        },
        waitForRun: async (id, status = 'completed') => {
          runIds.add(id);
          await expect
            .poll(async () => (await api<AEORun>('GET', `/aeo/runs/${id}`)).status, {
              intervals: [250, 500],
            })
            .toBe(status);
        },
      });
    } finally {
      try {
        await Promise.all(runResponses);
        for (const id of runIds) {
          await expect
            .poll(async () => (await api<AEORun>('GET', `/aeo/runs/${id}`)).status, {
              timeout: 15000,
              intervals: [500],
            })
            .not.toBe('running');
        }
        const current = await configurations();
        // Clear test keys before restoring endpoints. The stub stays alive
        // until all settings and owned prompts have been restored or removed.
        for (const saved of [...originals].sort(
          (left, right) => Number(Boolean(right.is_sensitive)) - Number(Boolean(left.is_sensitive)),
        )) {
          const updated = current.find((config) => config.key === saved.key);
          if (updated && (updated.value !== saved.value || updated.is_set)) {
            await api('PUT', `/configurations/${saved.key}`, { value: saved.value });
          }
        }
        for (const id of promptIds) await api('DELETE', `/aeo/prompts/${id}`, undefined, 204);
        if (originalProfile) await api('PUT', '/aeo/profile', originalProfile);
        // There is no profile-delete API. On a fresh DB the test profile and
        // completed runs remain in the runner's disposable evidence database.
      } finally {
        await stub.stop();
      }
    }
  },
});

export { expect } from '@playwright/test';
