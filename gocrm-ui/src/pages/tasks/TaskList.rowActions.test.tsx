import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { useNavigate } from 'react-router-dom';
import { Component as TaskList } from './TaskList';
import { labelsApi, tasksApi, type TaskFilters } from '@/api/endpoints';
import { createMockTask } from '@/test/factories';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  tasksApi: {
    getTasks: vi.fn(),
    deleteTask: vi.fn(),
    updateTask: vi.fn(),
  },
  labelsApi: {
    getLabels: vi.fn(),
  },
}));

// The button a user presses: the one inside the row that shows this text. It
// must also be named for that row, so a menu bound to another row's record
// (for instance through a stale index) fails here.
const menuButtonOnRow = async (text: string) => {
  const row = (await screen.findByText(text)).closest('tr');
  if (!row) throw new Error(`no row for ${text}`);
  const button = within(row).getByRole('button', { name: /^More actions for / });
  expect(button).toHaveAccessibleName(`More actions for ${text}`);
  return button;
};

// The per-row overflow menu carries the one task action the row icons do not:
// marking the task complete. It must act on the row whose button was pressed,
// whatever the current order of the table.
describe('TaskList row actions', () => {
  const mockNavigate = vi.fn();
  const tasks = [
    createMockTask({ id: 1, title: 'Alpha task', status: 'pending' }),
    createMockTask({ id: 2, title: 'Bravo task', status: 'in_progress' }),
    createMockTask({ id: 3, title: 'Charlie task', status: 'completed' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(labelsApi.getLabels).mockResolvedValue([]);
    // A sort reverses the rows and a search narrows them, so the tests act on
    // rows whose position changes.
    vi.mocked(tasksApi.getTasks).mockImplementation(async (filters?: TaskFilters) => {
      const term = filters?.search?.toLowerCase() ?? '';
      const matching = tasks.filter((task) => task.title.toLowerCase().includes(term));
      const data = filters?.sort_by ? [...matching].reverse() : matching;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
    vi.mocked(tasksApi.updateTask).mockResolvedValue(tasks[1]);
  });

  it('completes the chosen task after the table was re-sorted', async () => {
    render(<TaskList />);
    await screen.findByText('Alpha task');

    fireEvent.click(screen.getByRole('button', { name: 'Title' }));
    await waitFor(() => expect(screen.getAllByRole('row')[1]).toHaveTextContent('Charlie task'));

    // Alpha moved from the first row to the last.
    fireEvent.click(await menuButtonOnRow('Alpha task'));
    const menu = await screen.findByRole('menu', { name: 'More actions for Alpha task' });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Mark as Complete' }));

    await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
    expect(tasksApi.updateTask).toHaveBeenCalledWith(1, { status: 'completed' });
    // The menu item must not fall through to the row's own click.
    expect(mockNavigate).not.toHaveBeenCalledWith('/tasks/1');
  });

  it('completes the chosen task after a search narrowed the table', async () => {
    render(<TaskList />);
    await screen.findByText('Alpha task');

    fireEvent.change(screen.getByPlaceholderText('Search tasks...'), {
      target: { value: 'Bravo' },
    });
    await screen.findByText('Bravo task');
    expect(screen.queryByText('Alpha task')).not.toBeInTheDocument();

    // Bravo moved from the second row to the first.
    fireEvent.click(await menuButtonOnRow('Bravo task'));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Mark as Complete' }));

    await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
    expect(tasksApi.updateTask).toHaveBeenCalledWith(2, { status: 'completed' });
  });

  it('offers no menu on a completed task', async () => {
    render(<TaskList />);
    await screen.findByText('Charlie task');

    expect(
      screen.queryByRole('button', { name: 'More actions for Charlie task' }),
    ).not.toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: /^More actions for / })).toHaveLength(2);
  });

  it('has no row-less overflow button in the table toolbar', async () => {
    render(<TaskList />);
    await screen.findByText('Alpha task');

    for (const icon of screen.queryAllByTestId('MoreVertIcon')) {
      expect(icon.closest('button')).toHaveAccessibleName(/^More actions for /);
    }
  });

  it('opens and runs the menu from the keyboard', async () => {
    const user = userEvent.setup();
    render(<TaskList />);
    await screen.findByText('Alpha task');

    const trigger = await menuButtonOnRow('Alpha task');
    act(() => trigger.focus());
    await user.keyboard('{Enter}');
    const item = await screen.findByRole('menuitem', { name: 'Mark as Complete' });
    await waitFor(() => expect(item).toHaveFocus());
    await user.keyboard('{Enter}');

    await waitFor(() =>
      expect(tasksApi.updateTask).toHaveBeenCalledWith(1, { status: 'completed' }),
    );
  });
});
