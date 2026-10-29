import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { useNavigate } from 'react-router-dom';
import { Component as LeadList } from './LeadList';
import { leadsApi, type LeadFilters } from '@/api/endpoints';
import { createMockLead } from '@/test/factories';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  leadsApi: {
    getLeads: vi.fn(),
    deleteLead: vi.fn(),
    convertLead: vi.fn(),
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

// The per-row overflow menu carries the one lead action the row icons do not:
// conversion. It must act on the row whose button was pressed, whatever the
// current order of the table.
describe('LeadList row actions', () => {
  const mockNavigate = vi.fn();
  const leads = [
    createMockLead({
      id: 1,
      company_name: 'Alpha Leads',
      contact_name: 'Ann Alpha',
      status: 'new',
    }),
    createMockLead({
      id: 2,
      company_name: 'Bravo Leads',
      contact_name: 'Ben Bravo',
      status: 'qualified',
    }),
    createMockLead({
      id: 3,
      company_name: 'Charlie Leads',
      contact_name: 'Cy Charlie',
      status: 'qualified',
    }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    // Any sort returns the rows reversed and a search narrows them, so the
    // tests below act on rows whose position changes: an action bound to a
    // row index instead of the row record would pick the wrong lead.
    vi.mocked(leadsApi.getLeads).mockImplementation(async (filters?: LeadFilters) => {
      const term = filters?.search?.toLowerCase() ?? '';
      const matching = leads.filter((lead) => lead.company_name.toLowerCase().includes(term));
      const data = filters?.sort_by ? [...matching].reverse() : matching;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
    vi.mocked(leadsApi.convertLead).mockResolvedValue({ customer_id: 77 });
  });

  it('has no row-less overflow button in the table toolbar', async () => {
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    // Every remaining overflow button names its row.
    for (const button of screen.queryAllByTestId('MoreVertIcon')) {
      expect(button.closest('button')).toHaveAccessibleName(/^More actions for /);
    }
    expect(screen.getAllByRole('button', { name: /^More actions for / })).toHaveLength(2);
  });

  it('offers the menu only on convertible leads', async () => {
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    expect(
      screen.queryByRole('button', { name: 'More actions for Alpha Leads' }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'More actions for Bravo Leads' }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: 'More actions for Charlie Leads' }),
    ).toBeInTheDocument();
  });

  it('converts the chosen lead after the table was re-sorted', async () => {
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    fireEvent.click(screen.getByRole('button', { name: 'Company' }));
    await waitFor(() => expect(screen.getAllByRole('row')[1]).toHaveTextContent('Charlie Leads'));

    // Charlie moved from the last row to the first.
    fireEvent.click(await menuButtonOnRow('Charlie Leads'));
    const menu = await screen.findByRole('menu', { name: 'More actions for Charlie Leads' });
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Convert to Customer' }));

    // The menu item must not fall through to the row's own click.
    expect(mockNavigate).not.toHaveBeenCalledWith('/leads/3');

    const dialog = await screen.findByRole('dialog', { name: 'Convert Lead to Customer' });
    expect(dialog).toHaveTextContent('Cy Charlie');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));

    await waitFor(() => expect(leadsApi.convertLead).toHaveBeenCalledTimes(1));
    expect(vi.mocked(leadsApi.convertLead).mock.calls[0][0]).toBe(3);
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/customers/77'));
  });

  it('converts the chosen lead after a search narrowed the table', async () => {
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    fireEvent.change(screen.getByPlaceholderText('Search leads...'), {
      target: { value: 'Charlie' },
    });
    await screen.findByText('Charlie Leads');
    expect(screen.queryByText('Alpha Leads')).not.toBeInTheDocument();

    // Charlie is now the only, and so the first, row.
    fireEvent.click(await menuButtonOnRow('Charlie Leads'));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Convert to Customer' }));
    const dialog = await screen.findByRole('dialog', { name: 'Convert Lead to Customer' });
    expect(dialog).toHaveTextContent('Cy Charlie');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));

    await waitFor(() => expect(leadsApi.convertLead).toHaveBeenCalledTimes(1));
    expect(vi.mocked(leadsApi.convertLead).mock.calls[0][0]).toBe(3);
  });

  it('opens and runs the menu from the keyboard', async () => {
    const user = userEvent.setup();
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    const trigger = await menuButtonOnRow('Charlie Leads');
    act(() => trigger.focus());
    await user.keyboard('{Enter}');

    const item = await screen.findByRole('menuitem', { name: 'Convert to Customer' });
    await waitFor(() => expect(item).toHaveFocus());
    await user.keyboard('{Enter}');

    const dialog = await screen.findByRole('dialog', { name: 'Convert Lead to Customer' });
    expect(dialog).toHaveTextContent('Cy Charlie');
  });
});
