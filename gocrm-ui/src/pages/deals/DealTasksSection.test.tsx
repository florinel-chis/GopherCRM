import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { DealTasksSection } from './DealTasksSection';
import { tasksApi } from '@/api/endpoints';
import { createMockTask, createMockUser } from '@/test/factories';
import type { User } from '@/types';

vi.mock('@/api/endpoints', () => ({
  tasksApi: {
    getDealTasks: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn().mockResolvedValue([]),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
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

const deal = { id: 9 };

const section = async (name: string | RegExp): Promise<HTMLElement> =>
  (await screen.findByRole('heading', { name })).closest('section') as HTMLElement;

describe('DealTasksSection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
  });

  it('renders the tasks the API returned for the deal', async () => {
    vi.mocked(tasksApi.getDealTasks).mockResolvedValue({
      tasks: [
        createMockTask({ id: 31, title: 'Send the proposal', status: 'in_progress', deal_id: 9 }),
        createMockTask({ id: 32, title: 'Book the demo', status: 'completed', deal_id: 9 }),
      ],
      // The API total, larger than the page.
      total: 7,
    });

    render(<DealTasksSection deal={deal} />);

    const tasks = await section('Tasks (7)');
    expect(tasksApi.getDealTasks).toHaveBeenCalledWith(9, { offset: 0, limit: 5 });
    const rows = within(tasks).getAllByTestId('deal-task-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('link', { name: 'Send the proposal' })).toHaveAttribute(
      'href',
      '/tasks/31'
    );
    expect(within(rows[0]).getByTestId('deal-task-status')).toHaveTextContent('In Progress');
    expect(within(rows[1]).getByRole('link', { name: 'Book the demo' })).toHaveAttribute(
      'href',
      '/tasks/32'
    );
    expect(within(rows[1]).getByTestId('deal-task-status')).toHaveTextContent('Completed');
    expect(within(tasks).queryByText('No tasks for this deal')).not.toBeInTheDocument();
  });

  it('says so when the deal has no tasks', async () => {
    vi.mocked(tasksApi.getDealTasks).mockResolvedValue({ tasks: [], total: 0 });

    render(<DealTasksSection deal={deal} />);

    const tasks = await section('Tasks (0)');
    expect(within(tasks).getByText('No tasks for this deal')).toBeInTheDocument();
    expect(within(tasks).queryAllByTestId('deal-task-row')).toHaveLength(0);
  });

  it('requests the selected page with its offset and limit', async () => {
    vi.mocked(tasksApi.getDealTasks).mockResolvedValue({
      tasks: [createMockTask({ id: 31, title: 'Send the proposal', deal_id: 9 })],
      total: 30,
    });

    render(<DealTasksSection deal={deal} />);
    const tasks = await section('Tasks (30)');
    expect(tasksApi.getDealTasks).toHaveBeenLastCalledWith(9, { offset: 0, limit: 5 });

    fireEvent.click(within(tasks).getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(tasksApi.getDealTasks).toHaveBeenLastCalledWith(9, { offset: 5, limit: 5 })
    );

    fireEvent.mouseDown(within(tasks).getByRole('combobox'));
    fireEvent.click(await screen.findByRole('option', { name: '10' }));
    await waitFor(() =>
      expect(tasksApi.getDealTasks).toHaveBeenLastCalledWith(9, { offset: 0, limit: 10 })
    );
  });

  it('shows the assigned-to-you caption to sales only', async () => {
    vi.mocked(tasksApi.getDealTasks).mockResolvedValue({ tasks: [], total: 0 });

    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    const { unmount } = render(<DealTasksSection deal={deal} />);
    await section('Tasks (0)');
    expect(screen.getByText('Only tasks assigned to you are shown')).toBeInTheDocument();
    unmount();

    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    render(<DealTasksSection deal={deal} />);
    await section('Tasks (0)');
    expect(screen.queryByText('Only tasks assigned to you are shown')).not.toBeInTheDocument();
  });
});
