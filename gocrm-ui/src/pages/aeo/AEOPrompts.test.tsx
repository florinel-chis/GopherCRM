import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AxiosError } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as AEOPrompts, segmentMentions } from './AEOPrompts';
import { aeoApi } from '@/api/endpoints/aeo';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();
const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
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
  },
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const prompts = [
  {
    id: 1,
    text: 'Which CRM would you recommend?',
    is_active: true,
    created_at: '2026-08-01T00:00:00Z',
    visibility: 42.5,
    answer_count: 8,
    mention_count: 3,
    last_run_at: '2026-08-10T06:00:00Z',
  },
  {
    id: 2,
    text: 'Best CRM for small teams?',
    is_active: false,
    created_at: '2026-08-02T00:00:00Z',
    visibility: 0,
    answer_count: 0,
    mention_count: 0,
  },
];

const profile = {
  id: 1,
  brand_name: 'Acme',
  description: '',
  brand_aliases: ['Acme Inc'],
  owned_domains: ['acme.com'],
  competitors: [],
};

const answers = [
  {
    id: 11,
    run_id: 5,
    prompt_id: 1,
    provider: 'anthropic',
    model: 'claude-opus-5',
    attempt: 1,
    answer_text: 'Acme is a solid CRM, while Acmerica is unrelated.',
    brand_mentioned: true,
    first_mention_pos: 0,
    competitor_mentions: { Globex: 2 },
    latency_ms: 812,
    citations: [
      { id: 21, answer_id: 11, url: 'https://acme.com/crm', domain: 'acme.com', is_owned: true },
    ],
    created_at: '2026-08-10T06:00:10Z',
  },
  {
    id: 12,
    run_id: 5,
    prompt_id: 1,
    provider: 'openai',
    model: 'gpt-4o-mini',
    attempt: 1,
    answer_text: '',
    brand_mentioned: false,
    first_mention_pos: -1,
    competitor_mentions: {},
    latency_ms: 0,
    error: 'openai: 429 rate limited',
    citations: [],
    created_at: '2026-08-10T06:00:11Z',
  },
];

const runs = [
  {
    id: 5,
    trigger: 'scheduled' as const,
    status: 'completed' as const,
    started_at: '2026-08-10T06:00:00Z',
    completed_at: '2026-08-10T06:04:00Z',
    total_queries: 10,
    failed_queries: 1,
  },
];

const axiosErrorWithStatus = (status: number, message?: string): AxiosError => {
  const error = new AxiosError('request failed');
  error.response = {
    status,
    statusText: '',
    data: message ? { message } : {},
    headers: {},
    config: { headers: {} as never },
  } as never;
  return error;
};

const openDrawer = async () => {
  fireEvent.click(await screen.findByRole('button', { name: 'Which CRM would you recommend?' }));
  await waitFor(() => {
    expect(screen.getByTestId('answer-card-11')).toBeInTheDocument();
  });
};

describe('segmentMentions', () => {
  it('marks whole-word brand hits only', () => {
    const segments = segmentMentions('Acme is fine but Acmerica is not', ['Acme']);
    expect(segments.filter((segment) => segment.match).map((segment) => segment.text)).toEqual([
      'Acme',
    ]);
  });

  it('is case-insensitive and honours aliases', () => {
    const segments = segmentMentions('acme inc and ACME both count', ['Acme', 'Acme Inc']);
    expect(segments.filter((segment) => segment.match).map((segment) => segment.text)).toEqual([
      'acme inc',
      'ACME',
    ]);
  });

  it('returns the text untouched when there are no terms', () => {
    expect(segmentMentions('nothing here', [])).toEqual([{ text: 'nothing here', match: false }]);
  });
});

describe('AEOPrompts', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    (aeoApi.getPrompts as any).mockResolvedValue(prompts);
    (aeoApi.getProfile as any).mockResolvedValue(profile);
    (aeoApi.getRuns as any).mockResolvedValue(runs);
    (aeoApi.getPromptAnswers as any).mockResolvedValue(answers);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows the loading state while prompts are fetched', () => {
    (aeoApi.getPrompts as any).mockImplementation(() => new Promise(() => {}));

    render(<AEOPrompts />);

    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('renders an error alert when the prompt list fails', async () => {
    (aeoApi.getPrompts as any).mockRejectedValue(new Error('boom'));

    render(<AEOPrompts />);

    expect(await screen.findByText(/Failed to load AEO prompts/)).toBeInTheDocument();
  });

  it('renders an empty state when nothing is tracked yet', async () => {
    (aeoApi.getPrompts as any).mockResolvedValue([]);

    render(<AEOPrompts />);

    expect(await screen.findByText(/No AEO data yet — add a prompt/)).toBeInTheDocument();
  });

  it('lists prompts with visibility, counts and last run', async () => {
    render(<AEOPrompts />);

    await waitFor(() => {
      expect(aeoApi.getPrompts).toHaveBeenCalledWith({ days: 30 });
    });

    const row = (await screen.findByText('Which CRM would you recommend?')).closest('tr')!;
    expect(within(row).getByText('42.5%')).toBeInTheDocument();
    expect(within(row).getByText('8')).toBeInTheDocument();
    expect(within(row).getByText('3')).toBeInTheDocument();
    expect(screen.getByTestId('visibility-bar-1')).toBeInTheDocument();
  });

  it('refetches with the chosen reporting window', async () => {
    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');

    fireEvent.click(screen.getByRole('button', { name: '7 days' }));

    await waitFor(() => {
      expect(aeoApi.getPrompts).toHaveBeenCalledWith({ days: 7 });
    });
  });

  it('opens the detail drawer with one card per provider', async () => {
    render(<AEOPrompts />);

    await openDrawer();

    expect(aeoApi.getPromptAnswers).toHaveBeenCalledWith(1, { run_id: undefined, limit: 50 });

    const anthropicCard = screen.getByTestId('answer-card-11');
    expect(within(anthropicCard).getByText('anthropic')).toBeInTheDocument();
    expect(within(anthropicCard).getByText('Mentioned')).toBeInTheDocument();
    expect(within(anthropicCard).getByText(/claude-opus-5/)).toBeInTheDocument();
    expect(within(anthropicCard).getByText('Globex × 2')).toBeInTheDocument();
    expect(within(anthropicCard).getByText('acme.com (owned)')).toBeInTheDocument();

    const openaiCard = screen.getByTestId('answer-card-12');
    expect(within(openaiCard).getByText('No mentions')).toBeInTheDocument();
    expect(within(openaiCard).getByText('openai: 429 rate limited')).toBeInTheDocument();
    // A failed call has no transcript to show.
    expect(within(openaiCard).queryByTestId('answer-transcript')).not.toBeInTheDocument();
  });

  it('highlights brand mentions in the transcript without matching substrings', async () => {
    render(<AEOPrompts />);

    await openDrawer();

    const marks = screen.getAllByTestId('brand-mention');
    expect(marks).toHaveLength(1);
    expect(marks[0]).toHaveTextContent('Acme');
    expect(screen.getByTestId('answer-transcript')).toHaveTextContent(
      'Acme is a solid CRM, while Acmerica is unrelated.'
    );
  });

  it('runs a single prompt from the drawer', async () => {
    (aeoApi.runPrompt as any).mockResolvedValue({ id: 42, status: 'running' });
    render(<AEOPrompts />);

    await openDrawer();
    fireEvent.click(screen.getByTestId('run-single-prompt'));

    await waitFor(() => {
      expect(aeoApi.runPrompt).toHaveBeenCalledWith(1);
    });
  });

  it('hides the single-prompt run button from support', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'support' })));
    render(<AEOPrompts />);

    await openDrawer();
    expect(screen.queryByTestId('run-single-prompt')).not.toBeInTheDocument();
  });

  it('narrows the answers to a single run', async () => {
    render(<AEOPrompts />);

    await openDrawer();

    fireEvent.mouseDown(screen.getByLabelText('Run'));
    fireEvent.click(await screen.findByRole('option', { name: /#5/ }));

    await waitFor(() => {
      expect(aeoApi.getPromptAnswers).toHaveBeenCalledWith(1, { run_id: 5, limit: 50 });
    });
  });

  it('adds a batch of prompts, one per line', async () => {
    (aeoApi.createPrompts as any).mockResolvedValue([prompts[0]]);

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /add prompts/i }));

    fireEvent.change(await screen.findByLabelText(/^Prompts/), {
      target: { value: 'First question?\n\nSecond question?\n' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => {
      expect(aeoApi.createPrompts).toHaveBeenCalledWith([
        'First question?',
        'Second question?',
      ]);
    });
  });

  it('surfaces the 409 when a prompt text already exists', async () => {
    (aeoApi.createPrompts as any).mockRejectedValue(
      axiosErrorWithStatus(409, 'a prompt with this text already exists')
    );

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /add prompts/i }));

    fireEvent.change(await screen.findByLabelText(/^Prompts/), {
      target: { value: 'Which CRM would you recommend?' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('a prompt with this text already exists');
    });
  });

  // The active-prompt cap comes back as a 400 with the server's own wording.
  it('shows the server message when the active-prompt cap is hit', async () => {
    (aeoApi.createPrompts as any).mockRejectedValue(
      axiosErrorWithStatus(400, 'active prompt limit of 100 reached')
    );

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /add prompts/i }));

    fireEvent.change(await screen.findByLabelText(/^Prompts/), {
      target: { value: 'One more question?' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('active prompt limit of 100 reached');
    });
  });

  it('rejects an empty add-prompts submission client-side', async () => {
    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /add prompts/i }));

    fireEvent.click(await screen.findByRole('button', { name: 'Add' }));

    expect(await screen.findByText('Enter at least one prompt')).toBeInTheDocument();
    expect(aeoApi.createPrompts).not.toHaveBeenCalled();
  });

  it('saves the suggestions selected in the generate dialog', async () => {
    (aeoApi.generatePrompts as any).mockResolvedValue(['Generated one?', 'Generated two?']);
    (aeoApi.createPrompts as any).mockResolvedValue([prompts[0]]);

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /generate with ai/i }));
    fireEvent.click(await screen.findByRole('button', { name: /generate suggestions/i }));

    expect(await screen.findByText('Generated one?')).toBeInTheDocument();

    // Both arrive pre-selected; drop one before saving.
    fireEvent.click(screen.getByRole('checkbox', { name: 'Generated two?' }));
    fireEvent.click(screen.getByRole('button', { name: /add selected/i }));

    await waitFor(() => {
      expect(aeoApi.createPrompts).toHaveBeenCalledWith(['Generated one?']);
    });
  });

  it('reports a 503 from the generator as an unconfigured server', async () => {
    (aeoApi.generatePrompts as any).mockRejectedValue(axiosErrorWithStatus(503));

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getByRole('button', { name: /generate with ai/i }));
    fireEvent.click(await screen.findByRole('button', { name: /generate suggestions/i }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith(
        'Prompt generation is not configured on the server'
      );
    });
  });

  it('toggles a prompt active flag from the table', async () => {
    (aeoApi.updatePrompt as any).mockResolvedValue({ ...prompts[1], is_active: true });

    render(<AEOPrompts />);

    await screen.findByText('Best CRM for small teams?');
    fireEvent.click(screen.getByLabelText('Toggle Best CRM for small teams?'));

    await waitFor(() => {
      expect(aeoApi.updatePrompt).toHaveBeenCalledWith(2, { is_active: true });
    });
  });

  it('edits the prompt text through the dialog', async () => {
    (aeoApi.updatePrompt as any).mockResolvedValue({ ...prompts[0], text: 'Reworded?' });

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getAllByTestId('EditIcon')[0].closest('button')!);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Prompt text/)).toHaveValue(
        'Which CRM would you recommend?'
      );
    });

    fireEvent.change(screen.getByLabelText(/^Prompt text/), { target: { value: 'Reworded?' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(aeoApi.updatePrompt).toHaveBeenCalledWith(1, { text: 'Reworded?' });
    });
  });

  it('deletes a prompt after confirmation', async () => {
    (aeoApi.deletePrompt as any).mockResolvedValue(undefined);

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');
    fireEvent.click(screen.getAllByTestId('DeleteIcon')[0].closest('button')!);

    expect(
      await screen.findByText(/Answers already collected for it stay in the run history/)
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(aeoApi.deletePrompt).toHaveBeenCalledWith(1);
    });
  });

  it('offers add and edit to the sales role but not delete', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'sales' })));

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');

    expect(screen.getByRole('button', { name: /add prompts/i })).toBeInTheDocument();
    expect(screen.getAllByTestId('EditIcon')).toHaveLength(2);
    expect(screen.queryByTestId('DeleteIcon')).not.toBeInTheDocument();
  });

  it('renders read-only for the support role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'support' })));

    render(<AEOPrompts />);

    await screen.findByText('Which CRM would you recommend?');

    expect(screen.queryByRole('button', { name: /add prompts/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /generate with ai/i })).not.toBeInTheDocument();
    expect(screen.queryByTestId('EditIcon')).not.toBeInTheDocument();
    expect(screen.queryByTestId('DeleteIcon')).not.toBeInTheDocument();
    // The active flag becomes a plain chip rather than a switch.
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument();
    const activeRow = screen.getByText('Which CRM would you recommend?').closest('tr')!;
    const pausedRow = screen.getByText('Best CRM for small teams?').closest('tr')!;
    expect(within(activeRow).getByText('Active')).toBeInTheDocument();
    expect(within(pausedRow).getByText('Paused')).toBeInTheDocument();
  });
});
