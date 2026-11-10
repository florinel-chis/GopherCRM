import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { Component as TaskDetail } from './TaskDetail';
import { tasksApi } from '@/api/endpoints';
import { createMockTask, createMockUser } from '@/test/factories';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  tasksApi: {
    getTask: vi.fn(),
    updateTask: vi.fn(),
    deleteTask: vi.fn(),
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

describe('TaskDetail', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
  });

  describe('deal link', () => {
    it('shows the deal title and stage with a link when the API returned a deal summary', async () => {
      vi.mocked(tasksApi.getTask).mockResolvedValue(
        createMockTask({
          id: 5,
          title: 'Call the client',
          deal_id: 7,
          deal: { id: 7, title: 'Website redesign', stage: 'won' },
        })
      );

      render(<TaskDetail />);

      const block = await screen.findByTestId('task-deal');
      expect(tasksApi.getTask).toHaveBeenCalledWith(5);
      expect(within(block).getByText('Deal')).toBeInTheDocument();
      expect(within(block).getByRole('link', { name: 'Website redesign' })).toHaveAttribute(
        'href',
        '/deals/7'
      );
      expect(within(block).getByTestId('deal-stage-chip')).toHaveTextContent('Won');
    });

    it('shows no deal when the task has no deal summary', async () => {
      // An admin may get deal_id for a deleted deal, without a summary: the id
      // alone is never rendered.
      vi.mocked(tasksApi.getTask).mockResolvedValue(
        createMockTask({ id: 5, title: 'Call the client', deal_id: 7 })
      );

      render(<TaskDetail />);

      await screen.findByRole('heading', { name: 'Call the client' });
      expect(screen.queryByTestId('task-deal')).not.toBeInTheDocument();
      expect(screen.queryByText('Deal')).not.toBeInTheDocument();
      expect(screen.queryByTestId('deal-stage-chip')).not.toBeInTheDocument();
      const links = screen.queryAllByRole('link');
      expect(links.some((link) => link.getAttribute('href')?.startsWith('/deals'))).toBe(false);
    });
  });
});
