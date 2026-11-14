import { describe, it, expect, vi, beforeEach, afterEach, type Mock, type MockInstance } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as AEOPrompts } from './AEOPrompts';
import { aeoApi, type AEOAnswer, type AEOPrompt, type AEORun } from '@/api/endpoints/aeo';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/aeo', () => ({
  aeoApi: {
    getPrompts: vi.fn(),
    getProfile: vi.fn(),
    getRuns: vi.fn(),
    getPromptAnswers: vi.fn(),
    createPrompts: vi.fn(),
    updatePrompt: vi.fn(),
    deletePrompt: vi.fn(),
    generatePrompts: vi.fn(),
    runPrompt: vi.fn(),
    exportResults: vi.fn(),
  },
}));

const OBJECT_URL = 'blob:http://localhost/aeo-run-export';
const CSV_LABEL = 'Export run as CSV';
const JSON_LABEL = 'Export run as JSON';

const prompt: AEOPrompt = {
  id: 1,
  text: 'Which CRM would you recommend?',
  is_active: true,
  created_at: '2026-11-01T00:00:00Z',
  visibility: 50,
  answer_count: 1,
  mention_count: 1,
  last_run_at: '2026-11-10T06:00:00Z',
};

const answer: AEOAnswer = {
  id: 11,
  run_id: 5,
  prompt_id: 1,
  provider: 'anthropic',
  model: 'claude-opus-5',
  attempt: 1,
  answer_text: 'Acme is a solid CRM.',
  brand_mentioned: true,
  first_mention_pos: 0,
  competitor_mentions: {},
  latency_ms: 500,
  citations: [],
  created_at: '2026-11-10T06:00:10Z',
};

const runs: AEORun[] = [
  {
    id: 5,
    trigger: 'scheduled',
    status: 'completed',
    started_at: '2026-11-10T06:00:00Z',
    completed_at: '2026-11-10T06:04:00Z',
    total_queries: 10,
    failed_queries: 0,
  },
];

const signIn = (role: User['role']) => {
  mockUseAuth.mockReturnValue({
    user: createMockUser({ role }),
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });
};

const openDrawer = async () => {
  fireEvent.click(await screen.findByRole('button', { name: prompt.text }));
  await screen.findByTestId('answer-card-11');
};

const selectRun = async (id: number) => {
  // The run list loads once the drawer opens.
  await waitFor(() => expect(aeoApi.getRuns).toHaveBeenCalled());
  fireEvent.mouseDown(screen.getByLabelText('Run'));
  fireEvent.click(await screen.findByRole('option', { name: new RegExp(`#${id}`) }));
  await waitFor(() => expect(screen.getByRole('button', { name: CSV_LABEL })).toBeEnabled());
};

// An error shaped like the one axios rejects with for responseType 'blob'.
const axiosBlobError = (status: number, body: unknown) =>
  Object.assign(new Error(`Request failed with status code ${status}`), {
    isAxiosError: true,
    response: {
      status,
      data: new Blob([JSON.stringify(body)], { type: 'application/json' }),
    },
  });

interface ClickedAnchor {
  connected: boolean;
  href: string;
  download: string;
}

describe('AEOPrompts drawer export', () => {
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;
  let createObjectURL: Mock<(obj: Blob | MediaSource) => string>;
  let revokeObjectURL: Mock<(url: string) => void>;
  let anchorClick: MockInstance<() => void>;
  let clicked: ClickedAnchor[];

  beforeEach(() => {
    vi.clearAllMocks();
    signIn('admin');
    vi.mocked(aeoApi.getPrompts).mockResolvedValue([prompt]);
    vi.mocked(aeoApi.getProfile).mockResolvedValue(null);
    vi.mocked(aeoApi.getRuns).mockResolvedValue(runs);
    vi.mocked(aeoApi.getPromptAnswers).mockResolvedValue([answer]);

    createObjectURL = vi.fn((_obj: Blob | MediaSource) => OBJECT_URL);
    revokeObjectURL = vi.fn((_url: string) => {});
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;
    clicked = [];
    anchorClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      clicked.push({
        connected: this.isConnected && document.body.contains(this),
        href: this.getAttribute('href') ?? '',
        download: this.getAttribute('download') ?? '',
      });
    });
  });

  afterEach(() => {
    anchorClick.mockRestore();
    URL.createObjectURL = originalCreateObjectURL;
    URL.revokeObjectURL = originalRevokeObjectURL;
  });

  it('disables run export while all runs are selected', async () => {
    render(<AEOPrompts />);
    await openDrawer();

    const csv = screen.getByRole('button', { name: CSV_LABEL });
    const json = screen.getByRole('button', { name: JSON_LABEL });
    expect(csv).toBeDisabled();
    expect(json).toBeDisabled();
    expect(screen.getByText('Includes every prompt of the selected run.')).toBeInTheDocument();

    fireEvent.click(csv);
    fireEvent.click(json);

    expect(aeoApi.exportResults).not.toHaveBeenCalled();
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(clicked).toHaveLength(0);
  });

  it('exports the selected run as CSV and JSON', async () => {
    // Support may export although it cannot manage prompts.
    signIn('support');
    const csvBlob = new Blob(['run_id\n5\n'], { type: 'text/csv' });
    const jsonBlob = new Blob(['{"format_version":1}'], { type: 'application/json' });
    vi.mocked(aeoApi.exportResults).mockResolvedValueOnce(csvBlob).mockResolvedValueOnce(jsonBlob);

    render(<AEOPrompts />);
    await openDrawer();
    await selectRun(5);

    fireEvent.click(screen.getByRole('button', { name: CSV_LABEL }));
    await waitFor(() => expect(clicked).toHaveLength(1));
    await waitFor(() => expect(screen.getByRole('button', { name: JSON_LABEL })).toBeEnabled());
    fireEvent.click(screen.getByRole('button', { name: JSON_LABEL }));
    await waitFor(() => expect(clicked).toHaveLength(2));

    const calls = vi.mocked(aeoApi.exportResults).mock.calls.map(([params]) => params);
    expect(calls).toStrictEqual([
      { format: 'csv', run_id: 5 },
      { format: 'json', run_id: 5 },
    ]);
    calls.forEach((params) => expect('days' in params).toBe(false));

    expect(createObjectURL).toHaveBeenNthCalledWith(1, csvBlob);
    expect(createObjectURL).toHaveBeenNthCalledWith(2, jsonBlob);
    expect(clicked).toStrictEqual([
      { connected: true, href: OBJECT_URL, download: 'aeo-export-run-5.csv' },
      { connected: true, href: OBJECT_URL, download: 'aeo-export-run-5.json' },
    ]);
    expect(document.querySelector('a[download]')).toBeNull();
    await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledTimes(2));
    expect(revokeObjectURL).toHaveBeenCalledWith(OBJECT_URL);
    expect(showError).not.toHaveBeenCalled();
  });

  it('shows a vanished run and saves no file', async () => {
    vi.mocked(aeoApi.exportResults).mockRejectedValue(
      axiosBlobError(404, {
        success: false,
        error: { code: 'NOT_FOUND', message: 'AEO run not found' },
      }),
    );

    render(<AEOPrompts />);
    await openDrawer();
    await selectRun(5);

    fireEvent.click(screen.getByRole('button', { name: CSV_LABEL }));

    await waitFor(() => expect(showError).toHaveBeenCalledWith('This AEO run no longer exists.'));
    expect(aeoApi.exportResults).toHaveBeenCalledWith({ format: 'csv', run_id: 5 });
    expect(createObjectURL).not.toHaveBeenCalled();
    expect(revokeObjectURL).not.toHaveBeenCalled();
    expect(clicked).toHaveLength(0);
    expect(document.querySelector('a[download]')).toBeNull();
  });

  it('hides the run export controls from a customer', async () => {
    signIn('customer');

    render(<AEOPrompts />);
    await openDrawer();

    expect(screen.queryByRole('button', { name: CSV_LABEL })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: JSON_LABEL })).not.toBeInTheDocument();
    expect(screen.queryByTestId('aeo-run-export')).not.toBeInTheDocument();
    expect(aeoApi.exportResults).not.toHaveBeenCalled();
  });
});
