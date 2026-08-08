import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as TaskList } from './TaskList';
import { labelsApi, tasksApi } from '@/api/endpoints';
import { createMockLabel, createMockTask } from '@/test/factories';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
  };
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

const searchBox = () => screen.getByPlaceholderText('Search tasks...') as HTMLInputElement;

describe('TaskList label filter', () => {
  const mockNavigate = vi.fn();
  const onboarding = createMockLabel({ id: 4, name: 'Onboarding', color: '#1F77B4' });

  const mockTasks = [
    createMockTask({ id: 1, title: 'Prepare kickoff', labels: [onboarding] }),
    createMockTask({ id: 2, title: 'Send invoice', labels: [] }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (labelsApi.getLabels as any).mockResolvedValue([onboarding]);
    (tasksApi.getTasks as any).mockResolvedValue({
      data: mockTasks,
      total: mockTasks.length,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  const lastFilters = () => {
    const calls = (tasksApi.getTasks as any).mock.calls;
    return calls[calls.length - 1][0];
  };

  // The server applies label_id INSTEAD of search, so leaving a search term on
  // screen while a label filter is active would advertise a narrowing that is
  // not being applied.
  it('clears the search term when a label chip is used as a filter', async () => {
    render(<TaskList />);

    await waitFor(() => {
      expect(screen.getByText('Prepare kickoff')).toBeInTheDocument();
    });

    fireEvent.change(searchBox(), { target: { value: 'kickoff' } });
    await waitFor(() => {
      expect(lastFilters()).toMatchObject({ search: 'kickoff' });
    });
    // The new query key refetches, so wait for the rows before touching a chip.
    await waitFor(() => {
      expect(screen.getByText('Prepare kickoff')).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByText('Onboarding')[0]);

    await waitFor(() => {
      expect(lastFilters()).toMatchObject({ label_id: 4, search: '' });
    });
    await waitFor(() => {
      expect(searchBox().value).toBe('');
    });
  });

  it('disables the search box while a label filter is active and restores it on clear', async () => {
    render(<TaskList />);

    await waitFor(() => {
      expect(screen.getByText('Prepare kickoff')).toBeInTheDocument();
    });

    expect(searchBox()).not.toBeDisabled();

    fireEvent.click(screen.getAllByText('Onboarding')[0]);

    await waitFor(() => {
      expect(searchBox()).toBeDisabled();
    });
    expect(screen.getByText('Clear the label filter to search')).toBeInTheDocument();

    // Clearing the filter through the "Filtered by" chip hands search back.
    const activeFilter = screen.getByTestId('active-label-filter');
    fireEvent.click(activeFilter.querySelector('.MuiChip-deleteIcon')!);

    await waitFor(() => {
      expect(searchBox()).not.toBeDisabled();
    });
  });
});
