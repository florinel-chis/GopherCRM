import { describe, it, expect, vi, beforeEach } from 'vitest';
import React from 'react';
import { act, render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as LeadDetail } from './LeadDetail';
import { leadsApi } from '@/api/endpoints';
import { createMockLead, createMockCompany } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  leadsApi: {
    getLead: vi.fn(),
    deleteLead: vi.fn(),
    convertLead: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

// The lead-conversion settings the page reads from the configuration
// provider (GET /configurations/ui); each test sets its own.
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

describe('LeadDetail conversion', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    conversion.allowedStatuses = ['qualified'];
    conversion.requireNotes = false;
    conversion.autoAssignOwner = false;
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '1' });
    vi.mocked(leadsApi.getLead).mockResolvedValue(createMockLead({ status: 'qualified', email: '' }));
  });

  const openDialog = async () => {
    fireEvent.click(await screen.findByRole('button', { name: /Convert to Customer/i }));
    return screen.findByRole('dialog');
  };

  const convertThroughDialog = async () => {
    const dialog = await openDialog();
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));
    return dialog;
  };

  it("shows the server's reason in the dialog when a conversion is refused", async () => {
    const reason = 'This lead has no email address. Add one before converting it: every customer needs an email.';
    vi.mocked(leadsApi.convertLead).mockRejectedValue({ response: { status: 400, data: { message: reason } } });

    render(<LeadDetail />);
    const dialog = await convertThroughDialog();

    expect(await within(dialog).findByTestId('convert-lead-error')).toHaveTextContent(reason);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('falls back to a generic message when the server gives none', async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue(new Error('Network Error'));

    render(<LeadDetail />);
    const dialog = await convertThroughDialog();

    expect(await within(dialog).findByTestId('convert-lead-error')).toHaveTextContent('Failed to convert lead');
  });

  it('shows the server message for an already converted lead', async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: { status: 400, data: { code: 'BAD_REQUEST', message: 'lead already converted' } },
    });

    render(<LeadDetail />);
    const dialog = await convertThroughDialog();

    expect(await within(dialog).findByTestId('convert-lead-error')).toHaveTextContent('lead already converted');
    // The page re-reads the lead, whose status has moved on.
    await waitFor(() => expect(leadsApi.getLead).toHaveBeenCalledTimes(2));
  });

  it("offers conversion on the statuses the server allows, not a built-in list", async () => {
    conversion.allowedStatuses = ['contacted'];
    vi.mocked(leadsApi.getLead).mockResolvedValue(createMockLead({ status: 'contacted' }));
    const { unmount } = render(<LeadDetail />);
    expect(await screen.findByRole('button', { name: /Convert to Customer/i })).toBeInTheDocument();
    unmount();

    vi.mocked(leadsApi.getLead).mockResolvedValue(createMockLead({ status: 'qualified' }));
    render(<LeadDetail />);
    await screen.findByRole('button', { name: /Edit/i });
    expect(screen.queryByRole('button', { name: /Convert to Customer/i })).not.toBeInTheDocument();
  });

  it('requires notes when the setting is on and sends them', async () => {
    conversion.requireNotes = true;
    vi.mocked(leadsApi.convertLead).mockResolvedValue({ customer_id: 9 });

    render(<LeadDetail />);
    const dialog = await convertThroughDialog();

    expect(await within(dialog).findByText('Conversion notes are required.')).toBeInTheDocument();
    expect(leadsApi.convertLead).not.toHaveBeenCalled();

    fireEvent.change(within(dialog).getByLabelText(/Conversion Notes/), { target: { value: 'Signed' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));
    await waitFor(() =>
      expect(leadsApi.convertLead).toHaveBeenCalledWith(1, expect.objectContaining({ notes: 'Signed' })),
    );
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/customers/9'));
  });

  it("puts the server's notes refusal on the field and re-reads the settings", async () => {
    // The page still believed notes were optional.
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: {
        status: 400,
        data: { code: 'VALIDATION_ERROR', message: 'Conversion notes are required.', details: { field: 'notes' } },
      },
    });

    render(<LeadDetail />);
    const dialog = await convertThroughDialog();

    const notes = within(dialog).getByLabelText(/Conversion Notes/);
    await waitFor(() => expect(notes).toHaveAttribute('aria-invalid', 'true'));
    expect(within(dialog).getByText('Conversion notes are required.')).toBeInTheDocument();
    expect(conversion.refresh).toHaveBeenCalled();
    expect(showError).not.toHaveBeenCalled();
  });

  it('mentions owner assignment when the setting is on', async () => {
    conversion.autoAssignOwner = true;
    render(<LeadDetail />);
    const dialog = await openDialog();
    expect(within(dialog).getByText(/lead's owner becomes the customer's assigned user/i)).toBeInTheDocument();
  });
});

// The page stays mounted when the route moves from one lead to another.
describe('LeadDetail conversion across leads', () => {
  const mockNavigate = vi.fn();
  const leadA = createMockLead({ id: 1, company_name: 'Alpha Leads', contact_name: 'Ann Alpha', status: 'qualified' });
  const leadB = createMockLead({ id: 2, company_name: 'Bravo Leads', contact_name: 'Ben Bravo', status: 'qualified' });

  beforeEach(() => {
    vi.clearAllMocks();
    conversion.allowedStatuses = ['qualified'];
    conversion.requireNotes = false;
    conversion.autoAssignOwner = false;
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '1' });
    // A fresh object per read, as from the network.
    vi.mocked(leadsApi.getLead).mockImplementation(async (id: number) => ({ ...(id === 2 ? leadB : leadA) }));
  });

  const goTo = (rerender: (ui: React.ReactElement) => void, id: string) => {
    vi.mocked(useParams).mockReturnValue({ id });
    rerender(<LeadDetail />);
  };

  it("does not carry one lead's open dialog or refusal to the next lead", async () => {
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: { status: 400, data: { code: 'BAD_REQUEST', message: 'lead already converted' } },
    });
    const { rerender } = render(<LeadDetail />);
    fireEvent.click(await screen.findByRole('button', { name: /Convert to Customer/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));
    expect(await within(dialog).findByTestId('convert-lead-error')).toBeInTheDocument();

    goTo(rerender, '2');
    expect(await screen.findByRole('heading', { name: 'Bravo Leads' })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole('button', { name: /Convert to Customer/i }));
    const next = await screen.findByRole('dialog');
    expect(next).toHaveTextContent('Ben Bravo');
    expect(within(next).queryByTestId('convert-lead-error')).not.toBeInTheDocument();
  });

  it('keeps what the user typed when a refusal re-reads the same lead', async () => {
    // The re-read answers a changed record (it moved on meanwhile).
    let reads = 0;
    vi.mocked(leadsApi.getLead).mockImplementation(async () => {
      reads += 1;
      return { ...leadA, updated_at: reads === 1 ? leadA.updated_at : '2026-10-30T12:00:00Z' };
    });
    vi.mocked(leadsApi.convertLead).mockRejectedValue({
      response: { status: 400, data: { code: 'BAD_REQUEST', message: 'lead already converted' } },
    });
    render(<LeadDetail />);
    fireEvent.click(await screen.findByRole('button', { name: /Convert to Customer/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText(/Conversion Notes/), { target: { value: 'Typed note' } });
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));

    expect(await within(dialog).findByTestId('convert-lead-error')).toBeInTheDocument();
    // The refusal re-read the lead (a new object for the same id).
    await waitFor(() => expect(leadsApi.getLead).toHaveBeenCalledTimes(2));
    await act(async () => {});
    expect(within(screen.getByRole('dialog')).getByLabelText(/Conversion Notes/)).toHaveValue('Typed note');
  });

  it('does not open the customer when the conversion answers after the user moved to another lead', async () => {
    let answer: (value: { customer_id: number }) => void = () => {};
    vi.mocked(leadsApi.convertLead).mockImplementation(
      () => new Promise((resolve) => { answer = resolve; }),
    );
    const { rerender } = render(<LeadDetail />);
    fireEvent.click(await screen.findByRole('button', { name: /Convert to Customer/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));
    await waitFor(() => expect(leadsApi.convertLead).toHaveBeenCalledWith(1, expect.anything()));

    goTo(rerender, '2');
    expect(await screen.findByRole('heading', { name: 'Bravo Leads' })).toBeInTheDocument();
    await act(async () => answer({ customer_id: 9 }));

    await waitFor(() => expect(showSuccess).toHaveBeenCalled());
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});

describe('LeadDetail company', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '1' });
  });

  it('links to the company record when the lead is linked', async () => {
    vi.mocked(leadsApi.getLead).mockResolvedValue(
      createMockLead({
        company_name: 'Acme (as typed)',
        company_id: 7,
        company_record: createMockCompany({ id: 7, name: 'Acme Widgets' }),
      })
    );

    render(<LeadDetail />);

    const link = await screen.findByRole('link', { name: 'Acme Widgets' });
    expect(link).toHaveAttribute('href', '/companies/7');
  });

  it('shows the free-text company when there is no link', async () => {
    vi.mocked(leadsApi.getLead).mockResolvedValue(createMockLead({ company_name: 'Loose Text Ltd' }));

    render(<LeadDetail />);

    // The header and the Company field both carry the text; neither is a link.
    expect((await screen.findAllByText('Loose Text Ltd')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('link', { name: 'Loose Text Ltd' })).not.toBeInTheDocument();
  });
});
