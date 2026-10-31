import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as LeadList } from './LeadList';
import { leadsApi } from '@/api/endpoints';
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

// The lead-conversion settings from GET /configurations/ui, as the
// configuration provider hands them to the page.
const conversion = vi.hoisted(() => ({
  allowedStatuses: ['qualified'] as string[],
  requireNotes: false,
  autoAssignOwner: false,
  refresh: vi.fn(async () => {}),
}));

vi.mock('@/hooks/useConfiguration', () => ({
  useConfiguration: () => ({
    getLeadConversion: () => ({
      status: 'ready',
      settings: {
        allowedStatuses: conversion.allowedStatuses,
        requireNotes: conversion.requireNotes,
        autoAssignOwner: conversion.autoAssignOwner,
      },
    }),
    refreshConfigurations: conversion.refresh,
  }),
}));

describe('LeadList conversion settings', () => {
  const leads = [
    createMockLead({ id: 1, company_name: 'Alpha Leads', contact_name: 'Ann Alpha', status: 'new' }),
    createMockLead({ id: 2, company_name: 'Bravo Leads', contact_name: 'Ben Bravo', status: 'qualified' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    conversion.allowedStatuses = ['qualified'];
    conversion.requireNotes = false;
    conversion.autoAssignOwner = false;
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(leadsApi.getLeads).mockResolvedValue({
      data: leads,
      total: leads.length,
      page: 1,
      limit: 10,
      total_pages: 1,
    });
  });

  const openConvertDialog = async (company: string) => {
    fireEvent.click(await screen.findByRole('button', { name: `More actions for ${company}` }));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Convert to Customer' }));
    return screen.findByRole('dialog', { name: 'Convert Lead to Customer' });
  };

  it('offers conversion on the statuses the server allows', async () => {
    conversion.allowedStatuses = ['new'];
    render(<LeadList />);
    await screen.findByText('Alpha Leads');

    expect(screen.getByRole('button', { name: 'More actions for Alpha Leads' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'More actions for Bravo Leads' })).not.toBeInTheDocument();
  });

  it('requires notes when the setting is on', async () => {
    conversion.requireNotes = true;
    render(<LeadList />);
    const dialog = await openConvertDialog('Bravo Leads');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));
    expect(await within(dialog).findByText('Conversion notes are required.')).toBeInTheDocument();
    expect(leadsApi.convertLead).not.toHaveBeenCalled();
  });

  it('keeps the dialog open with the server refusal and clears it on close', async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: { status: 400, data: { code: 'BAD_REQUEST', message: 'lead already converted' } },
    });
    render(<LeadList />);
    let dialog = await openConvertDialog('Bravo Leads');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));
    expect(await within(dialog).findByTestId('convert-lead-error')).toHaveTextContent('lead already converted');
    // The list is re-read: the row's status has moved on.
    await waitFor(() => expect(leadsApi.getLeads).toHaveBeenCalledTimes(2));

    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    dialog = await openConvertDialog('Bravo Leads');
    expect(within(dialog).queryByTestId('convert-lead-error')).not.toBeInTheDocument();
  });

  it("re-reads the settings when the server refuses the lead's status", async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: {
        status: 400,
        data: {
          code: 'VALIDATION_ERROR',
          message: 'Leads with status "qualified" cannot be converted. Allowed statuses: contacted.',
          details: { field: 'status', status: 'qualified', allowed_statuses: ['contacted'] },
        },
      },
    });
    render(<LeadList />);
    const dialog = await openConvertDialog('Bravo Leads');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));
    expect(await within(dialog).findByTestId('convert-lead-error')).toHaveTextContent(
      'Leads with status "qualified" cannot be converted.',
    );
    expect(conversion.refresh).toHaveBeenCalled();
  });
  it('stops an open dialog from converting once the refreshed settings no longer allow the status', async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: {
        status: 400,
        data: {
          code: 'VALIDATION_ERROR',
          message: 'Leads with status "qualified" cannot be converted.',
          details: { field: 'status', status: 'qualified', allowed_statuses: [] },
        },
      },
    });
    conversion.refresh.mockImplementationOnce(async () => {
      conversion.allowedStatuses = [];
    });
    render(<LeadList />);
    const dialog = await openConvertDialog('Bravo Leads');

    fireEvent.click(within(dialog).getByRole('button', { name: 'Convert to Customer' }));
    expect(await within(dialog).findByTestId('convert-lead-error')).toBeInTheDocument();
    await waitFor(() =>
      expect(within(dialog).getByRole('button', { name: 'Convert to Customer' })).toBeDisabled(),
    );
    expect(within(dialog).getByTestId('convert-lead-ineligible')).toHaveTextContent(
      'The conversion settings do not allow converting a lead with status "qualified".',
    );
    expect(leadsApi.convertLead).toHaveBeenCalledTimes(1);
  });
});
