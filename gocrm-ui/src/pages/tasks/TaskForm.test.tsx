import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { useNavigate } from 'react-router-dom';
import { Component as TaskForm } from './TaskForm';
import { labelsApi, tasksApi, usersApi } from '@/api/endpoints';
import { createMockLabel, createMockUser } from '@/test/factories';
import type { User } from '@/types';
import { AxiosError } from 'axios';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: () => ({}),
  };
});

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/api/endpoints', () => ({
  tasksApi: {
    getTask: vi.fn(),
    createTask: vi.fn(),
    updateTask: vi.fn(),
  },
  usersApi: {
    getUsers: vi.fn(),
  },
  labelsApi: {
    getLabels: vi.fn(),
    createLabel: vi.fn(),
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

describe('TaskForm inline label creation', () => {
  const existing = createMockLabel({ id: 4, name: 'Onboarding', color: '#1F77B4' });

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(vi.fn());
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    (usersApi.getUsers as any).mockResolvedValue({ data: [], total: 0 });
    (labelsApi.getLabels as any).mockResolvedValue([existing]);
    (tasksApi.getTask as any).mockResolvedValue(undefined);
  });

  const typeIntoLabels = async (value: string) => {
    const user = userEvent.setup();
    const input = screen.getByLabelText('Labels');
    await user.click(input);
    await user.type(input, value);
  };

  it.each(['admin', 'sales', 'support'] as const)(
    'offers the inline create option to the %s role',
    async (role) => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));
      (labelsApi.createLabel as any).mockResolvedValue(
        createMockLabel({ id: 9, name: 'Renewals', color: '#2CA02C' })
      );

      render(<TaskForm />);

      await waitFor(() => {
        expect(screen.getByLabelText('Labels')).toBeInTheDocument();
      });

      await typeIntoLabels('Renewals');

      const option = await screen.findByText('Add "Renewals"');
      fireEvent.click(option);

      await waitFor(() => {
        expect(labelsApi.createLabel).toHaveBeenCalledWith(
          expect.objectContaining({ name: 'Renewals' })
        );
      });
    }
  );

  // POST /labels is admin/sales/support only, so offering a customer an option
  // that can only ever 403 is a dead affordance.
  it('hides the inline create option from the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 5, role: 'customer' })));

    render(<TaskForm />);

    await waitFor(() => {
      expect(screen.getByLabelText('Labels')).toBeInTheDocument();
    });

    await typeIntoLabels('Renewals');

    await waitFor(() => {
      expect(screen.getByText('Pick from the existing labels')).toBeInTheDocument();
    });
    expect(screen.queryByText('Add "Renewals"')).not.toBeInTheDocument();
    expect(labelsApi.createLabel).not.toHaveBeenCalled();
  });

  // A 409 means someone else created the label since our list was cached; the
  // form should select the real label rather than dead-end on an error.
  it('recovers from a duplicate-name 409 by selecting the existing label', async () => {
    const serverLabel = createMockLabel({ id: 12, name: 'renewals', color: '#D62728' });
    (labelsApi.getLabels as any)
      .mockResolvedValueOnce([existing])
      .mockResolvedValue([existing, serverLabel]);
    (labelsApi.createLabel as any).mockRejectedValue(
      new AxiosError('Conflict', 'ERR_BAD_REQUEST', undefined, undefined, {
        status: 409,
      } as any)
    );

    render(<TaskForm />);

    await waitFor(() => {
      expect(screen.getByLabelText('Labels')).toBeInTheDocument();
    });

    await typeIntoLabels('Renewals');

    const option = await screen.findByText('Add "Renewals"');
    fireEvent.click(option);

    expect(await screen.findByText('renewals')).toBeInTheDocument();
    expect(labelsApi.createLabel).toHaveBeenCalledTimes(1);
  });

  it('still offers existing labels to the customer role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 5, role: 'customer' })));

    render(<TaskForm />);

    await waitFor(() => {
      expect(screen.getByLabelText('Labels')).toBeInTheDocument();
    });

    await typeIntoLabels('Onboard');

    expect(await screen.findByText('Onboarding')).toBeInTheDocument();
  });
});
