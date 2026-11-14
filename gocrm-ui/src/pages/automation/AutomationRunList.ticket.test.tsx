import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { automationApi, configurationsApi } from '@/api/endpoints';
import { createMockAutomationRun, createMockUser } from '@/test/factories';
import type { User } from '@/types';
import { Component as AutomationRunList } from './AutomationRunList';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    listRuns: vi.fn(),
    listRules: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
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

describe('AutomationRunList', () => {
  describe('ticket runs', () => {
    const ticketRun = createMockAutomationRun({
      id: 21,
      status: 'completed',
      rule_id: 8,
      rule_name: 'Resolved tickets',
      rule_version: 1,
      entity_type: 'ticket',
      entity_id: 42,
    });

    beforeEach(() => {
      vi.clearAllMocks();
      window.history.replaceState({}, '', '/automation/runs');
      vi.mocked(useNavigate).mockReturnValue(vi.fn());
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
      vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [ticketRun], total: 1 });
      vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [], total: 0 });
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
    });

    it('the entity filter offers Ticket and a ticket run links to its ticket', async () => {
      render(<AutomationRunList />);

      const row = (await screen.findByText('Resolved tickets · v1')).closest('tr') as HTMLElement;
      expect(within(row).getByRole('link', { name: 'Ticket #42' })).toHaveAttribute('href', '/tickets/42');

      fireEvent.mouseDown(screen.getByRole('combobox', { name: /^Entity/ }));
      const listbox = await screen.findByRole('listbox');
      expect(within(listbox).getAllByRole('option').map((option) => option.textContent)).toEqual([
        'All',
        'Lead',
        'Deal',
        'Ticket',
      ]);
      fireEvent.click(within(listbox).getByRole('option', { name: 'Ticket' }));

      await waitFor(() =>
        expect(automationApi.listRuns).toHaveBeenLastCalledWith(
          expect.objectContaining({ offset: 0, entity_type: 'ticket' }),
        ),
      );
      expect(new URLSearchParams(window.location.search).get('entity_type')).toBe('ticket');
      expect(screen.getByRole('combobox', { name: /^Entity/ })).toHaveTextContent('Ticket');
      expect(screen.getByLabelText('Record ID')).toBeEnabled();
    });
  });
});
