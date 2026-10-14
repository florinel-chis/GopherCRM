import type { Page } from '@playwright/test';
import type { AEOAnswer, AEOProviderStatus, AEORun } from '../../src/api/endpoints/aeo';
import { test, expect } from '../fixtures/aeo';
import { LoginPage } from '../pages/login.page';

const providers = [
  { id: 'anthropic', name: 'Anthropic', key: 'anthropic' },
  { id: 'openai', name: 'OpenAI', key: 'openai' },
  { id: 'gemini', name: 'Gemini', key: 'gemini' },
  { id: 'kimi', name: 'Kimi', key: 'moonshot' },
  { id: 'perplexity', name: 'Perplexity', key: 'perplexity' },
] as const;

async function saveConnection(page: Page, name: string, model: string, baseURL: string) {
  const oldBaseURL = await page
    .getByRole('textbox', { name: `${name} base URL`, exact: true })
    .inputValue();
  const lastKey = `integration.aeo.${name.toLowerCase()}_${oldBaseURL === baseURL ? 'model' : 'base_url'}`;
  await page.getByRole('textbox', { name: `${name} model`, exact: true }).fill(model);
  await page.getByRole('textbox', { name: `${name} base URL`, exact: true }).fill(baseURL);
  const savedResponse = page.waitForResponse(
    (response) =>
      response.url().endsWith(`/configurations/${lastKey}`) &&
      response.request().method() === 'PUT',
  );
  await page.getByRole('button', { name: `Save ${name} connection`, exact: true }).click();
  expect((await savedResponse).status()).toBe(200);
  await expect(
    page.getByRole('button', { name: `Save ${name} connection`, exact: true }),
  ).toBeDisabled();
  await expect(
    page.getByRole('alert').filter({ hasText: `${name} connection saved` }),
  ).toBeVisible();
}

async function openGeneration(page: Page) {
  await page.goto('/aeo/prompts');
  await page.getByRole('button', { name: 'Generate with AI', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Generate suggestions' }).click();
}

test.describe('AEO provider connections with real SDKs and loopback providers', () => {
  test.setTimeout(90000);
  // Direct Playwright runs often target an existing development DB. Only the
  // runner, after resetting its DB, opts in to these global-setting mutations.
  test.skip(
    process.env.E2E_AEO_STUBS !== '1',
    'Run with make e2e to use a fresh database and disable live provider keys and scheduling',
  );

  test('all five connections persist through reload and clearing restores defaults', async ({
    page,
    aeo,
  }) => {
    await page.goto('/aeo/settings');
    for (const provider of providers) {
      await saveConnection(
        page,
        provider.name,
        `e2e-${provider.id}-model`,
        aeo.stub.baseURL(provider.id),
      );
    }
    await page.reload();
    const stored = await aeo.configurations();
    for (const provider of providers) {
      await expect(
        page.getByRole('textbox', { name: `${provider.name} model`, exact: true }),
      ).toHaveValue(`e2e-${provider.id}-model`);
      await expect(
        page.getByRole('textbox', { name: `${provider.name} base URL`, exact: true }),
      ).toHaveValue(aeo.stub.baseURL(provider.id));
      expect(
        stored.find((config) => config.key === `integration.aeo.${provider.id}_model`)?.value,
      ).toBe(`e2e-${provider.id}-model`);
      expect(
        stored.find((config) => config.key === `integration.aeo.${provider.id}_base_url`)?.value,
      ).toBe(aeo.stub.baseURL(provider.id));
      await saveConnection(page, provider.name, '', '');
    }
    await page.reload();
    const cleared = await aeo.configurations();
    for (const provider of providers) {
      await expect(
        page.getByRole('textbox', { name: `${provider.name} model`, exact: true }),
      ).toHaveValue('');
      await expect(
        page.getByRole('textbox', { name: `${provider.name} base URL`, exact: true }),
      ).toHaveValue('');
      expect(
        cleared.find((config) => config.key === `integration.aeo.${provider.id}_model`)?.value,
      ).toBe('');
      expect(
        cleared.find((config) => config.key === `integration.aeo.${provider.id}_base_url`)?.value,
      ).toBe('');
      const original = aeo.originalProviders.find((status) => status.name === provider.id);
      await expect(page.getByTestId(`provider-chip-${provider.id}`)).toHaveText(
        `${provider.id}: ${original?.model}`,
      );
    }
    expect(await aeo.api<AEOProviderStatus[]>('GET', '/aeo/providers')).toEqual(
      aeo.originalProviders,
    );
    expect(aeo.stub.requests).toEqual([]);
  });

  for (const provider of providers.filter(({ id }) => id === 'anthropic' || id === 'gemini')) {
    test(`${provider.name} uses saved connections for generation and picks up edits on the next run`, async ({
      page,
      aeo,
    }) => {
      await aeo.api('PUT', '/aeo/profile', {
        brand_name: 'E2E GopherCRM',
        description: 'CRM for small sales teams',
        brand_aliases: [],
        owned_domains: [],
        competitors: [],
      });
      await page.goto('/aeo/settings');
      await saveConnection(page, provider.name, 'e2e-first-model', aeo.stub.baseURL(provider.id));
      const dummyKey = `not-a-real-${provider.id}-e2e-key`;
      await page.getByLabel(`${provider.name} API key`, { exact: true }).fill(dummyKey);
      await page
        .getByRole('button', { name: `Save ${provider.name} API key`, exact: true })
        .click();
      await expect(page.getByTestId(`api-key-chip-${provider.name.toLowerCase()}`)).toHaveText(
        'Configured',
      );
      await expect(page.getByLabel(`${provider.name} API key`, { exact: true })).toHaveValue('');
      await page.getByRole('combobox', { name: 'Prompt generation engine' }).click();
      await page.getByRole('option', { name: provider.name, exact: true }).click();
      await expect(page.getByRole('combobox', { name: 'Prompt generation engine' })).toHaveText(
        provider.name,
      );

      await page.reload();
      await expect(page.getByTestId(`api-key-chip-${provider.name.toLowerCase()}`)).toHaveText(
        'Configured',
      );
      expect(
        (await aeo.configurations()).find(
          (config) => config.key === `integration.aeo.${provider.key}_api_key`,
        )?.value,
      ).toBe('');
      await openGeneration(page);
      await expect(
        page.getByRole('dialog').getByRole('checkbox', { name: aeo.stub.suggestion }),
      ).toBeChecked();
      const generationPath = provider.id === 'anthropic' ? 'v1/messages' : 'chat/completions';
      expect(aeo.stub.requests).toEqual([
        expect.objectContaining({
          path: `/${provider.id}/first/${generationPath}`,
          model: 'e2e-first-model',
          apiKey: dummyKey,
          prompt: expect.stringContaining('Brand: E2E GopherCRM'),
        }),
      ]);
      expect(await aeo.api('GET', '/aeo/prompts')).toEqual([]);
      await page.getByRole('dialog').getByRole('button', { name: 'Cancel', exact: true }).click();

      await page.goto('/aeo/settings');
      await saveConnection(
        page,
        provider.name,
        'e2e-second-model',
        aeo.stub.baseURL(provider.id, 'second'),
      );
      const promptText = `Which CRM should my ${provider.id} E2E sales team use?`;
      const promptId = await aeo.createPrompt(promptText);
      await page.goto('/aeo/prompts');
      await page.getByRole('button', { name: promptText, exact: true }).click();
      const runResponse = page.waitForResponse(
        (response) =>
          response.url().endsWith(`/aeo/prompts/${promptId}/run`) &&
          response.request().method() === 'POST',
      );
      await page.getByRole('button', { name: 'Run this prompt', exact: true }).click();
      const response = await runResponse;
      expect(response.status()).toBe(202);
      const run: AEORun = (await response.json()).data;
      await aeo.waitForRun(run.id);
      const answers = await aeo.api<AEOAnswer[]>(
        'GET',
        `/aeo/prompts/${promptId}/answers?run_id=${run.id}`,
      );
      expect(answers).toHaveLength(1);
      expect(answers[0]).toMatchObject({
        provider: provider.id,
        model: 'e2e-second-model',
        answer_text: aeo.stub.answer,
        brand_mentioned: true,
      });
      expect(answers[0].error || '').toBe('');
      expect(aeo.stub.requests[1]).toEqual({
        path: `/${provider.id}/second/${generationPath}`,
        model: 'e2e-second-model',
        prompt: promptText,
        apiKey: dummyKey,
      });
      await page.reload();
      await page.getByRole('button', { name: promptText, exact: true }).click();
      const card = page.getByTestId(`answer-card-${answers[0].id}`);
      await expect(card).toContainText('e2e-second-model');
      await expect(card).toContainText(aeo.stub.answer);
      await expect(card).toContainText('Mentioned');
    });
  }

  test('Gemini array-shaped 503 errors survive retries and appear in generation and tracked answers', async ({
    page,
    aeo,
  }) => {
    aeo.stub.rejectGemini = true;
    await aeo.api('PUT', '/aeo/profile', {
      brand_name: 'E2E GopherCRM',
      description: '',
      brand_aliases: [],
      owned_domains: [],
      competitors: [],
    });
    await aeo.api('PUT', '/configurations/integration.aeo.gemini_base_url', {
      value: aeo.stub.baseURL('gemini'),
    });
    await aeo.api('PUT', '/configurations/integration.aeo.gemini_model', {
      value: 'e2e-error-model',
    });
    await aeo.api('PUT', '/configurations/integration.aeo.gemini_api_key', {
      value: 'not-a-real-gemini-e2e-key',
    });
    await aeo.api('PUT', '/configurations/integration.aeo.generation_engine', { value: 'gemini' });
    const generationResponse = page.waitForResponse(
      (response) =>
        response.url().endsWith('/aeo/prompts/generate') && response.request().method() === 'POST',
    );
    await openGeneration(page);
    const response = await generationResponse;
    expect(response.status()).toBe(503);
    expect((await response.json()).error.code).toBe('PROVIDER_REJECTED');
    await expect(
      page.getByText('the generation engine is unavailable (HTTP 503); try again later', {
        exact: true,
      }),
    ).toBeVisible();
    expect(aeo.stub.requests.length).toBeGreaterThan(1);

    const promptText = 'Which CRM should the failing Gemini E2E team use?';
    const promptId = await aeo.createPrompt(promptText);
    const run = await aeo.api<AEORun>('POST', `/aeo/prompts/${promptId}/run`, undefined, 202);
    await aeo.waitForRun(run.id, 'failed');
    expect(await aeo.api('GET', `/aeo/runs/${run.id}`)).toMatchObject({
      total_queries: 1,
      failed_queries: 1,
    });
    const answers = await aeo.api<AEOAnswer[]>(
      'GET',
      `/aeo/prompts/${promptId}/answers?run_id=${run.id}`,
    );
    expect(answers).toHaveLength(1);
    expect(answers[0]).toMatchObject({
      provider: 'gemini',
      model: 'e2e-error-model',
      answer_text: '',
    });
    expect(answers[0].error).toContain(aeo.stub.rejectionMessage);
    expect(aeo.stub.requests.filter((query) => query.prompt === promptText).length).toBeGreaterThan(
      1,
    );
    await page.goto('/aeo/prompts');
    await page.getByRole('button', { name: promptText, exact: true }).click();
    await expect(page.getByTestId(`answer-card-${answers[0].id}`).getByRole('alert')).toContainText(
      aeo.stub.rejectionMessage,
    );
  });

  test('connection settings remain admin-only while staff and customer access matches their roles', async ({
    page,
    aeo,
  }) => {
    const userIds: number[] = [];
    try {
      for (const role of ['sales', 'support', 'customer'] as const) {
        const email = `aeo-${role}-${Date.now()}@example.com`;
        const password = 'AeoRolePass123!';
        const user = await aeo.api<{ id: number }>(
          'POST',
          '/users',
          {
            first_name: 'AEO',
            last_name: role,
            email,
            password,
            role,
          },
          201,
        );
        userIds.push(user.id);
        const loginPage = new LoginPage(page);
        await page.evaluate(() => {
          localStorage.clear();
          sessionStorage.clear();
        });
        await loginPage.goto();
        await loginPage.emailInput.fill(email);
        await loginPage.passwordInput.fill(password);
        await loginPage.rememberMeCheckbox.check();
        await loginPage.submit();
        await page.waitForURL('/');
        const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
        if (!token) throw new Error(`No token for ${role}`);
        expect(
          (
            await aeo.response('GET', '/configurations/category/integration', undefined, token)
          ).status(),
        ).toBe(403);
        expect(
          (
            await aeo.response(
              'PUT',
              '/configurations/integration.aeo.gemini_model',
              { value: 'unauthorized-model' },
              token,
            )
          ).status(),
        ).toBe(403);
        await page.goto('/aeo/settings');
        if (role === 'customer') {
          await expect(page).toHaveURL(/\/unauthorized$/);
          expect((await aeo.response('GET', '/aeo/providers', undefined, token)).status()).toBe(
            403,
          );
        } else {
          await expect(
            page.getByRole('heading', { name: 'AEO Settings', exact: true }),
          ).toBeVisible();
          await expect(
            page.getByRole('textbox', { name: 'Gemini model', exact: true }),
          ).toHaveCount(0);
          await expect(page.getByLabel('Gemini API key', { exact: true })).toHaveCount(0);
          if (role === 'support') {
            await expect(page.getByRole('button', { name: 'Run now', exact: true })).toHaveCount(0);
            await expect(page.getByRole('textbox', { name: 'Brand name' })).toBeDisabled();
            expect(
              (await aeo.response('POST', '/aeo/prompts/generate', { count: 1 }, token)).status(),
            ).toBe(403);
          } else {
            await expect(page.getByRole('button', { name: 'Run now', exact: true })).toBeVisible();
            await expect(page.getByRole('textbox', { name: 'Brand name' })).toBeEnabled();
          }
        }
        await page.evaluate(() => {
          localStorage.clear();
          sessionStorage.clear();
        });
      }
    } finally {
      for (const id of userIds) await aeo.api('DELETE', `/users/${id}`, undefined, 204);
    }
    expect(aeo.stub.requests).toEqual([]);
  });
});
