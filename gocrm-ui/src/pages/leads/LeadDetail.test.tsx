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

vi.mock('@/contexts/ConfigurationContext', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('@/contexts/ConfigurationContext');
  return {
    ...actual,
    useConfiguration: () => ({ getLeadConversionStatuses: () => ['qualified'] }),
  };
});

describe('LeadDetail conversion', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (useParams as any).mockReturnValue({ id: '1' });
    (leadsApi.getLead as any).mockResolvedValue(createMockLead({ status: 'qualified', email: '' }));
  });

  const convertThroughDialog = async () => {
    fireEvent.click(await screen.findByRole('button', { name: /Convert to Customer/i }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: /Convert to Customer/i }));
  };

  it("shows the server's reason when a conversion is refused", async () => {
    const reason = 'This lead has no email address. Add one before converting it: every customer needs an email.';
    (leadsApi.convertLead as any).mockRejectedValue({ response: { status: 400, data: { message: reason } } });

    render(<LeadDetail />);
    await convertThroughDialog();

    await waitFor(() => expect(showError).toHaveBeenCalledWith(reason));
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('falls back to a generic message when the server gives none', async () => {
    (leadsApi.convertLead as any).mockRejectedValue(new Error('Network Error'));

    render(<LeadDetail />);
    await convertThroughDialog();

    await waitFor(() => expect(showError).toHaveBeenCalledWith('Failed to convert lead'));
  });
});

describe('LeadDetail company', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(vi.fn());
    (useParams as any).mockReturnValue({ id: '1' });
  });

  it('links to the company record when the lead is linked', async () => {
    (leadsApi.getLead as any).mockResolvedValue(
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
    (leadsApi.getLead as any).mockResolvedValue(createMockLead({ company_name: 'Loose Text Ltd' }));

    render(<LeadDetail />);

    // The header and the Company field both carry the text; neither is a link.
    expect((await screen.findAllByText('Loose Text Ltd')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('link', { name: 'Loose Text Ltd' })).not.toBeInTheDocument();
  });
});
