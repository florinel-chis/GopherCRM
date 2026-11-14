import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { automationApi, configurationsApi } from '@/api/endpoints';
import { createMockAutomationRule, createMockUser } from '@/test/factories';
import type { User } from '@/types';
import { Component as AutomationRuleList } from './AutomationRuleList';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    listRules: vi.fn(),
    enableRule: vi.fn(),
    disableRule: vi.fn(),
    deleteRule: vi.fn(),
    dryRun: vi.fn(),
    dryRunDraft: vi.fn(),
  },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
  ticketsApi: { getTickets: vi.fn(), getTicket: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
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

const rowOf = async (text: string) => (await screen.findByText(text)).closest('tr') as HTMLElement;

describe('AutomationRuleList', () => {
  describe('ticket rules', () => {
    beforeEach(() => {
      vi.clearAllMocks();
      vi.mocked(useNavigate).mockReturnValue(vi.fn());
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
      vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
    });

    it('a ticket rule is labelled Ticket', async () => {
      vi.mocked(automationApi.listRules).mockResolvedValue({
        rules: [
          createMockAutomationRule({
            id: 1,
            name: 'Resolved tickets',
            entity_type: 'ticket',
            trigger_type: 'ticket_status_changed',
            trigger: { hour_utc: 0, from: 'open', to: 'resolved' },
          }),
          createMockAutomationRule({
            id: 2,
            name: 'Stale tickets',
            entity_type: 'ticket',
            trigger_type: 'schedule',
            trigger: { hour_utc: 8, filters: { ticket_status: 'in_progress' } },
          }),
          createMockAutomationRule({ id: 3, name: 'Welcome leads', entity_type: 'lead' }),
        ],
        total: 3,
      });
      render(<AutomationRuleList />);

      const changed = await rowOf('Resolved tickets');
      expect(within(changed).getByText('Ticket')).toBeInTheDocument();
      expect(within(changed).getByText('Ticket status changed: open → resolved')).toBeInTheDocument();

      const schedule = await rowOf('Stale tickets');
      expect(within(schedule).getByText('Ticket')).toBeInTheDocument();
      expect(within(schedule).getByText('Schedule, daily at 08:00 UTC, status in_progress')).toBeInTheDocument();

      // Lead rules keep their label.
      const lead = await rowOf('Welcome leads');
      expect(within(lead).getByText('Lead')).toBeInTheDocument();
      expect(within(lead).getByText('Lead created')).toBeInTheDocument();
    });
  });
});
