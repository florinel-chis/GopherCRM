import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as LabelList } from './LabelList';
import { labelsApi } from '@/api/endpoints';
import { createMockLabel, createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/api/endpoints', () => ({
  labelsApi: {
    getLabels: vi.fn(),
    createLabel: vi.fn(),
    updateLabel: vi.fn(),
    deleteLabel: vi.fn(),
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

describe('LabelList', () => {
  const mockLabels = [
    createMockLabel({ id: 1, name: 'Onboarding', color: '#1F77B4', task_count: 3 }),
    createMockLabel({ id: 2, name: 'Escalation', color: '#D62728', task_count: 1 }),
    createMockLabel({ id: 3, name: 'Backlog', color: '#BCBD22', task_count: 0 }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    (labelsApi.getLabels as any).mockResolvedValue(mockLabels);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders the label table with name, colour swatch and task count', async () => {
    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Labels')).toBeInTheDocument();
    });

    expect(screen.getByText('Onboarding')).toBeInTheDocument();
    expect(screen.getByText('Escalation')).toBeInTheDocument();
    expect(screen.getByText('Backlog')).toBeInTheDocument();

    expect(screen.getByText('#1F77B4')).toBeInTheDocument();
    expect(screen.getByTestId('label-swatch-1')).toBeInTheDocument();

    const onboardingRow = screen.getByText('Onboarding').closest('tr');
    expect(onboardingRow).toHaveTextContent('3');
  });

  it('shows the loading state while the labels are fetched', () => {
    (labelsApi.getLabels as any).mockImplementation(() => new Promise(() => {}));

    render(<LabelList />);

    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('renders an empty state when there are no labels', async () => {
    (labelsApi.getLabels as any).mockResolvedValue([]);

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('No labels yet.')).toBeInTheDocument();
    });
  });

  it('creates a label through the dialog', async () => {
    (labelsApi.createLabel as any).mockResolvedValue(
      createMockLabel({ id: 9, name: 'Renewals', color: '#2CA02C', task_count: 0 })
    );

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Onboarding')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /create label/i }));

    await waitFor(() => {
      expect(within(screen.getByRole('dialog')).getByText('Create Label')).toBeInTheDocument();
    });

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Renewals' } });
    fireEvent.click(screen.getByRole('button', { name: 'Use color #2CA02C' }));
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => {
      expect(labelsApi.createLabel).toHaveBeenCalledWith({
        name: 'Renewals',
        color: '#2CA02C',
      });
    });
  });

  it('preloads the dialog with the label being edited', async () => {
    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Escalation')).toBeInTheDocument();
    });

    const editButtons = screen.getAllByTestId('EditIcon');
    fireEvent.click(editButtons[1].closest('button')!);

    await waitFor(() => {
      expect(screen.getByText('Edit Label')).toBeInTheDocument();
    });

    expect(screen.getByLabelText(/^Name/)).toHaveValue('Escalation');
    expect(screen.getByLabelText(/^Hex color/)).toHaveValue('#D62728');
  });

  it('states the number of tasks a delete will detach the label from', async () => {
    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Onboarding')).toBeInTheDocument();
    });

    const deleteButtons = screen.getAllByTestId('DeleteIcon');
    fireEvent.click(deleteButtons[0].closest('button')!);

    await waitFor(() => {
      expect(
        screen.getByText(/It will be removed from 3 tasks\./)
      ).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(labelsApi.deleteLabel).toHaveBeenCalledWith(1);
    });
  });

  it('uses the singular form when a label is attached to one task', async () => {
    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Escalation')).toBeInTheDocument();
    });

    const deleteButtons = screen.getAllByTestId('DeleteIcon');
    fireEvent.click(deleteButtons[1].closest('button')!);

    await waitFor(() => {
      expect(screen.getByText(/It will be removed from 1 task\./)).toBeInTheDocument();
    });
  });

  // Tasks embed their labels, and the detail/edit pages cache a single task
  // under ['task', id] — a key the ['tasks'] list prefix does not match. Both
  // have to be invalidated or a renamed label keeps its old name on a task page
  // for as long as the query stays fresh.
  it('invalidates both task caches after a label is renamed', async () => {
    const invalidateQueries = vi.spyOn(QueryClient.prototype, 'invalidateQueries');
    (labelsApi.updateLabel as any).mockResolvedValue(
      createMockLabel({ id: 2, name: 'Escalated', color: '#D62728', task_count: 1 })
    );

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Escalation')).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByTestId('EditIcon')[1].closest('button')!);

    await waitFor(() => {
      expect(screen.getByText('Edit Label')).toBeInTheDocument();
    });

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: 'Escalated' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => {
      expect(labelsApi.updateLabel).toHaveBeenCalled();
    });

    await waitFor(() => {
      expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['task'] });
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['tasks'] });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['labels'] });
  });

  it('invalidates both task caches after a label is deleted', async () => {
    const invalidateQueries = vi.spyOn(QueryClient.prototype, 'invalidateQueries');
    (labelsApi.deleteLabel as any).mockResolvedValue(undefined);

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Onboarding')).toBeInTheDocument();
    });

    fireEvent.click(screen.getAllByTestId('DeleteIcon')[0].closest('button')!);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));

    await waitFor(() => {
      expect(labelsApi.deleteLabel).toHaveBeenCalledWith(1);
    });

    await waitFor(() => {
      expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['task'] });
    });
    expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: ['tasks'] });
  });

  it.each(['admin', 'sales', 'support'] as const)(
    'offers create and edit to the %s role',
    async (role) => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));

      render(<LabelList />);

      await waitFor(() => {
        expect(screen.getByText('Onboarding')).toBeInTheDocument();
      });

      expect(screen.getByRole('button', { name: /create label/i })).toBeInTheDocument();
      expect(screen.getAllByTestId('EditIcon').length).toBe(3);
    }
  );

  it('hides create, edit and delete from the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 5, role: 'customer' })));

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Onboarding')).toBeInTheDocument();
    });

    expect(screen.queryByRole('button', { name: /create label/i })).not.toBeInTheDocument();
    expect(screen.queryByTestId('EditIcon')).not.toBeInTheDocument();
    expect(screen.queryByTestId('DeleteIcon')).not.toBeInTheDocument();
  });

  it('offers delete only to an admin', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'support' })));

    render(<LabelList />);

    await waitFor(() => {
      expect(screen.getByText('Onboarding')).toBeInTheDocument();
    });

    expect(screen.queryByTestId('DeleteIcon')).not.toBeInTheDocument();
  });
});
