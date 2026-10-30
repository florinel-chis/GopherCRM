import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
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
    getLeadConversionStatuses: () => conversion.allowedStatuses,
    getLeadConversionSettings: () => ({
      allowedStatuses: conversion.allowedStatuses,
      requireNotes: conversion.requireNotes,
      autoAssignOwner: conversion.autoAssignOwner,
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
