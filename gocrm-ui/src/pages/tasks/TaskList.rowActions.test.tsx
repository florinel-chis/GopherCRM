import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
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
    vi.mocked(tasksApi.getTasks).mockImplementation(async (filters?: TaskFilters) => {
      const data = filters?.sort_by ? [...tasks].reverse() : tasks;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
    vi.mocked(tasksApi.updateTask).mockResolvedValue(tasks[1]);
  });

  it('completes the chosen task after the table was re-sorted', async () => {
    render(<TaskList />);
    await screen.findByText('Alpha task');

    fireEvent.click(screen.getByRole('button', { name: 'Title' }));
    await waitFor(() => expect(screen.getAllByRole('row')[1]).toHaveTextContent('Charlie task'));

    fireEvent.click(screen.getByRole('button', { name: 'More actions for Bravo task' }));
    const menu = await screen.findByRole('menu', { name: 'More actions for Bravo task' });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Mark as Complete' }));

    await waitFor(() =>
      expect(tasksApi.updateTask).toHaveBeenCalledWith(2, { status: 'completed' }),
    );
    // The menu item must not fall through to the row's own click.
    expect(mockNavigate).not.toHaveBeenCalledWith('/tasks/2');
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

    screen.getByRole('button', { name: 'More actions for Alpha task' }).focus();
    await user.keyboard('{Enter}');
    const item = await screen.findByRole('menuitem', { name: 'Mark as Complete' });
    await waitFor(() => expect(item).toHaveFocus());
    await user.keyboard('{Enter}');

    await waitFor(() =>
      expect(tasksApi.updateTask).toHaveBeenCalledWith(1, { status: 'completed' }),
    );
  });
});
