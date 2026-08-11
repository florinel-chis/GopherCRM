import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError } from 'axios';
import { aeoApi } from './aeo';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendProfile = {
  id: 1,
  brand_name: 'Acme',
  description: 'CRM for gophers',
  brand_aliases: ['Acme Inc'],
  owned_domains: ['acme.com'],
  competitors: [{ name: 'Globex', aliases: ['Globex Corp'], domain: 'globex.com' }],
};

const backendPrompt = {
  id: 7,
  text: 'Which CRM would you recommend?',
  is_active: true,
  created_at: '2026-08-01T00:00:00Z',
  visibility: 42.5,
  answer_count: 8,
  mention_count: 3,
  last_run_at: '2026-08-10T06:00:00Z',
};

// The client peels { success, data }; a 404 keeps the raw AxiosError shape.
const axiosErrorWithStatus = (status: number): AxiosError => {
  const error = new AxiosError('request failed');
  error.response = {
    status,
    statusText: '',
    data: {},
    headers: {},
    config: { headers: {} as never },
  } as never;
  return error;
};

describe('aeoApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('reads the brand profile from /aeo/profile', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: backendProfile });

    const profile = await aeoApi.getProfile();

    expect(api.get).toHaveBeenCalledWith('/aeo/profile');
    expect(profile?.brand_name).toBe('Acme');
    expect(profile?.competitors[0].domain).toBe('globex.com');
  });

  // 404 is the unconfigured-yet state, not a failure.
  it('returns null when the profile has not been configured', async () => {
    vi.mocked(api.get).mockRejectedValue(axiosErrorWithStatus(404));

    await expect(aeoApi.getProfile()).resolves.toBeNull();
  });

  it('rethrows non-404 failures from the profile endpoint', async () => {
    const boom = axiosErrorWithStatus(500);
    vi.mocked(api.get).mockRejectedValue(boom);

    await expect(aeoApi.getProfile()).rejects.toBe(boom);
  });

  it('saves the brand profile with PUT', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: backendProfile });

    const saved = await aeoApi.saveProfile({
      brand_name: 'Acme',
      description: 'CRM for gophers',
      brand_aliases: ['Acme Inc'],
      owned_domains: ['acme.com'],
      competitors: [{ name: 'Globex', aliases: ['Globex Corp'], domain: 'globex.com' }],
    });

    expect(api.put).toHaveBeenCalledWith('/aeo/profile', {
      brand_name: 'Acme',
      description: 'CRM for gophers',
      brand_aliases: ['Acme Inc'],
      owned_domains: ['acme.com'],
      competitors: [{ name: 'Globex', aliases: ['Globex Corp'], domain: 'globex.com' }],
    });
    expect(saved.id).toBe(1);
  });

  it('lists prompts with the window and paging params', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendPrompt] });

    const prompts = await aeoApi.getPrompts({ days: 30, active_only: true, limit: 25 });

    expect(api.get).toHaveBeenCalledWith('/aeo/prompts', {
      params: { days: 30, active_only: true, limit: 25 },
    });
    expect(prompts).toHaveLength(1);
    expect(prompts[0].visibility).toBe(42.5);
  });

  it('returns an empty prompt list when the response is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(aeoApi.getPrompts()).resolves.toEqual([]);
  });

  it('creates prompts from a batch of texts', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: [backendPrompt] });

    const created = await aeoApi.createPrompts(['Which CRM would you recommend?']);

    expect(api.post).toHaveBeenCalledWith('/aeo/prompts', {
      prompts: ['Which CRM would you recommend?'],
    });
    expect(created[0].id).toBe(7);
  });

  it('propagates a 409 duplicate-prompt rejection to the caller', async () => {
    const conflict = axiosErrorWithStatus(409);
    vi.mocked(api.post).mockRejectedValue(conflict);

    await expect(aeoApi.createPrompts(['dup'])).rejects.toBe(conflict);
  });

  it('updates and deletes a prompt by id', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendPrompt, is_active: false } });
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    const updated = await aeoApi.updatePrompt(7, { is_active: false });
    await aeoApi.deletePrompt(7);

    expect(api.put).toHaveBeenCalledWith('/aeo/prompts/7', { is_active: false });
    expect(updated.is_active).toBe(false);
    expect(api.delete).toHaveBeenCalledWith('/aeo/prompts/7');
  });

  it('reads generated suggestions out of the prompts field', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { prompts: ['Best CRM for gophers?'] } });

    const suggestions = await aeoApi.generatePrompts(5);

    expect(api.post).toHaveBeenCalledWith('/aeo/prompts/generate', { count: 5 });
    expect(suggestions).toEqual(['Best CRM for gophers?']);
  });

  it('returns no suggestions when the generate payload is malformed', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} });

    await expect(aeoApi.generatePrompts()).resolves.toEqual([]);
  });

  it('fetches per-prompt answers scoped to a run', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [] });

    await aeoApi.getPromptAnswers(7, { run_id: 3, limit: 20 });

    expect(api.get).toHaveBeenCalledWith('/aeo/prompts/7/answers', {
      params: { run_id: 3, limit: 20 },
    });
  });

  it('starts a run and lists run history', async () => {
    const run = {
      id: 12,
      trigger: 'manual',
      status: 'running',
      started_at: '2026-08-11T09:00:00Z',
      total_queries: 0,
      failed_queries: 0,
    };
    vi.mocked(api.post).mockResolvedValue({ data: run });
    vi.mocked(api.get).mockResolvedValue({ data: [run] });

    expect((await aeoApi.createRun()).id).toBe(12);
    expect(api.post).toHaveBeenCalledWith('/aeo/runs');

    expect(await aeoApi.getRuns({ limit: 10 })).toHaveLength(1);
    expect(api.get).toHaveBeenCalledWith('/aeo/runs', { params: { limit: 10 } });
  });

  it('reads a single run by id', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { id: 12 } });

    await aeoApi.getRun(12);

    expect(api.get).toHaveBeenCalledWith('/aeo/runs/12');
  });

  it('passes the day window to the dashboard and citations reports', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: {} });

    await aeoApi.getDashboard(7);
    expect(api.get).toHaveBeenCalledWith('/aeo/dashboard', { params: { days: 7 } });

    await aeoApi.getCitations(90);
    expect(api.get).toHaveBeenCalledWith('/aeo/citations', { params: { days: 90 } });
  });

  it('lists provider statuses', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [{ name: 'anthropic', model: 'claude-opus-5', configured: true }],
    });

    const providers = await aeoApi.getProviders();

    expect(api.get).toHaveBeenCalledWith('/aeo/providers');
    expect(providers[0].configured).toBe(true);
  });

  it('returns an empty provider list when the response is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: null });

    await expect(aeoApi.getProviders()).resolves.toEqual([]);
  });
});
