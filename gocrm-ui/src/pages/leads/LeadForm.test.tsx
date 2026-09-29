import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as LeadForm } from './LeadForm';
import { leadsApi, companiesApi } from '@/api/endpoints';
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
    createLead: vi.fn(),
    updateLead: vi.fn(),
  },
  companiesApi: {
    getCompanies: vi.fn(),
    getCompany: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
  }),
}));

describe('LeadForm', () => {
  const mockNavigate = vi.fn();

  const acme = createMockCompany({ id: 7, name: 'Acme Widgets', domain: 'acme.example' });

  // The rejection the form sees for an unknown company_id: the API answers
  // `{success:false, error:{code:"INVALID_REFERENCE", message, details:null}}`
  // and the axios client (src/api/client.ts) replaces `response.data` with
  // that `error` object before the endpoint module rethrows.
  const invalidReference = (message: string) => ({
    response: { status: 400, data: { code: 'INVALID_REFERENCE', message, details: null } },
  });

  const pickCompany = async () => {
    const input = screen.getByLabelText('Company (linked)');
    input.focus();
    fireEvent.change(input, { target: { value: 'acme' } });
    fireEvent.click(await screen.findByRole('option', { name: 'Acme Widgets (acme.example)' }));
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    vi.mocked(companiesApi.getCompanies).mockResolvedValue({ companies: [acme], total: 1 });
    vi.mocked(companiesApi.getCompany).mockResolvedValue(acme);
  });

  describe('Create Mode', () => {
    it('renders create form', () => {
      render(<LeadForm />);

      expect(screen.getByText('Create New Lead')).toBeInTheDocument();
      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Contact Name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Email/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Phone/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Status/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Lead Source/i)).toBeInTheDocument();
    });

    it('submits form with valid data', async () => {
      vi.mocked(leadsApi.createLead).mockResolvedValue(createMockLead());
      
      render(<LeadForm />);

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
        target: { value: 'New Company' },
      });
      fireEvent.change(screen.getByLabelText(/Contact Name/i), {
        target: { value: 'John Doe' },
      });
      fireEvent.change(screen.getByLabelText(/Email/i), {
        target: { value: 'john@newcompany.com' },
      });
      fireEvent.change(screen.getByLabelText(/Phone/i), {
        target: { value: '+1234567890' },
      });

      // Select source
      const sourceSelect = screen.getByLabelText(/Lead Source/i);
      fireEvent.mouseDown(sourceSelect);
      await waitFor(() => {
        const websiteOption = screen.getByRole('option', { name: 'Website' });
        fireEvent.click(websiteOption);
      });

      const submitButton = screen.getByText('Create Lead');
      fireEvent.click(submitButton);

      await waitFor(() => {
        expect(leadsApi.createLead).toHaveBeenCalledWith({
          company_name: 'New Company',
          contact_name: 'John Doe',
          email: 'john@newcompany.com',
          phone: '+1234567890',
          status: 'new',
          source: 'website',
          notes: '',
        });
        expect(mockNavigate).toHaveBeenCalledWith('/leads');
      });
    });

    it('prevents submission with invalid data', async () => {
      render(<LeadForm />);

      // Click submit without filling any fields
      const submitButton = screen.getByText('Create Lead');
      fireEvent.click(submitButton);

      // Wait a moment for validation to process
      await waitFor(() => {
        // The form should not have called the API if validation failed
        expect(leadsApi.createLead).not.toHaveBeenCalled();
      });

      // Fill in some fields but leave email invalid
      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
        target: { value: 'Test Company' },
      });
      fireEvent.change(screen.getByLabelText(/Contact Name/i), {
        target: { value: 'Test Contact' },
      });
      fireEvent.change(screen.getByLabelText(/Email/i), {
        target: { value: 'invalid-email' },
      });
      fireEvent.change(screen.getByLabelText(/Phone/i), {
        target: { value: '+123456789' },
      });

      // Select source
      const sourceSelect = screen.getByLabelText(/Lead Source/i);
      fireEvent.mouseDown(sourceSelect);
      const websiteOption = await screen.findByRole('option', { name: 'Website' });
      fireEvent.click(websiteOption);

      // Try to submit again
      fireEvent.click(submitButton);

      await waitFor(() => {
        // Should still not call API due to invalid email
        expect(leadsApi.createLead).not.toHaveBeenCalled();
      });
    });

    it('renders both company fields with distinct labels', () => {
      render(<LeadForm />);

      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toBeInTheDocument();
      expect(screen.getByLabelText('Company (linked)')).toBeInTheDocument();
    });

    it('sends company_id when a company is linked and seeds the blank free-text company', async () => {
      vi.mocked(leadsApi.createLead).mockResolvedValue(createMockLead());

      render(<LeadForm />);

      await pickCompany();
      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toHaveValue('Acme Widgets');

      fireEvent.change(screen.getByLabelText(/Contact Name/i), { target: { value: 'John Doe' } });
      fireEvent.change(screen.getByLabelText(/Email/i), { target: { value: 'john@acme.example' } });
      fireEvent.mouseDown(screen.getByLabelText(/Lead Source/i));
      fireEvent.click(await screen.findByRole('option', { name: 'Website' }));

      fireEvent.click(screen.getByText('Create Lead'));

      await waitFor(() => {
        expect(leadsApi.createLead).toHaveBeenCalledWith(
          expect.objectContaining({ company_name: 'Acme Widgets', company_id: 7 })
        );
      });
    });

    it('puts an INVALID_REFERENCE message on the linked-company picker', async () => {
      const message = 'unknown company_id 7: company not found';
      vi.mocked(leadsApi.createLead).mockRejectedValue(invalidReference(message));

      render(<LeadForm />);

      await pickCompany();
      fireEvent.change(screen.getByLabelText(/Contact Name/i), { target: { value: 'John Doe' } });
      fireEvent.change(screen.getByLabelText(/Email/i), { target: { value: 'john@acme.example' } });
      fireEvent.mouseDown(screen.getByLabelText(/Lead Source/i));
      fireEvent.click(await screen.findByRole('option', { name: 'Website' }));

      fireEvent.click(screen.getByText('Create Lead'));

      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(screen.getByLabelText('Company (linked)')).toHaveAccessibleDescription(message);
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('does not overwrite a free-text company the user already typed', async () => {
      render(<LeadForm />);

      fireEvent.change(screen.getByLabelText(/^Company \(as entered\)/i), {
        target: { value: 'Acme (Cluj branch)' },
      });
      await pickCompany();

      expect(screen.getByLabelText(/^Company \(as entered\)/i)).toHaveValue('Acme (Cluj branch)');
    });

    it('navigates back on cancel', () => {
      render(<LeadForm />);

      const cancelButton = screen.getByText('Cancel');
      fireEvent.click(cancelButton);

      expect(mockNavigate).toHaveBeenCalledWith('/leads');
    });
  });

  describe('Edit Mode', () => {
    const mockLead = createMockLead({
      id: 1,
      company_name: 'Existing Company',
      contact_name: 'Jane Smith',
      email: 'jane@existing.com',
      phone: '+0987654321',
      status: 'contacted',
      source: 'referral',
      notes: 'Important lead',
    });

    beforeEach(() => {
      vi.mocked(useParams).mockReturnValue({ id: '1' });
      vi.mocked(leadsApi.getLead).mockResolvedValue(mockLead);
    });

    it('renders edit form with existing data', async () => {
      render(<LeadForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Lead')).toBeInTheDocument();
        expect(screen.getByDisplayValue('Existing Company')).toBeInTheDocument();
        expect(screen.getByDisplayValue('Jane Smith')).toBeInTheDocument();
        expect(screen.getByDisplayValue('jane@existing.com')).toBeInTheDocument();
        expect(screen.getByDisplayValue('+0987654321')).toBeInTheDocument();
        expect(screen.getByDisplayValue('Important lead')).toBeInTheDocument();
      });
    });

    it('updates lead successfully', async () => {
      vi.mocked(leadsApi.updateLead).mockResolvedValue(mockLead);
      
      render(<LeadForm />);

      await waitFor(() => {
        expect(screen.getByDisplayValue('Existing Company')).toBeInTheDocument();
      });

      // Update company name
      const companyInput = screen.getByLabelText(/^Company \(as entered\)/i);
      fireEvent.change(companyInput, {
        target: { value: 'Updated Company' },
      });

      const submitButton = screen.getByText('Update Lead');
      fireEvent.click(submitButton);

      await waitFor(() => {
        expect(leadsApi.updateLead).toHaveBeenCalledWith(1, {
          company_name: 'Updated Company',
          contact_name: 'Jane Smith',
          email: 'jane@existing.com',
          phone: '+0987654321',
          status: 'contacted',
          source: 'referral',
          notes: 'Important lead',
        });
        expect(mockNavigate).toHaveBeenCalledWith('/leads');
      });
    });

    it('omits company_id when the lead had no link and none is chosen', async () => {
      vi.mocked(leadsApi.updateLead).mockResolvedValue(mockLead);

      render(<LeadForm />);
      await waitFor(() => expect(screen.getByDisplayValue('Existing Company')).toBeInTheDocument());

      fireEvent.click(screen.getByText('Update Lead'));

      await waitFor(() => expect(leadsApi.updateLead).toHaveBeenCalled());
      expect(vi.mocked(leadsApi.updateLead).mock.calls[0][1]).not.toHaveProperty('company_id');
    });

    it('preselects the linked company and sends its id back untouched', async () => {
      const linked = createMockLead({ ...mockLead, company_id: 7, company_record: acme });
      vi.mocked(leadsApi.getLead).mockResolvedValue(linked);
      vi.mocked(leadsApi.updateLead).mockResolvedValue(linked);

      render(<LeadForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
      });
      expect(companiesApi.getCompany).not.toHaveBeenCalled();

      fireEvent.click(screen.getByText('Update Lead'));

      await waitFor(() => {
        expect(leadsApi.updateLead).toHaveBeenCalledWith(1, expect.objectContaining({ company_id: 7 }));
      });
    });

    it('sends company_id 0 when a previously linked company is cleared', async () => {
      const linked = createMockLead({ ...mockLead, company_id: 7, company_record: acme });
      vi.mocked(leadsApi.getLead).mockResolvedValue(linked);
      vi.mocked(leadsApi.updateLead).mockResolvedValue(linked);

      render(<LeadForm />);
      await waitFor(() => {
        expect(screen.getByLabelText('Company (linked)')).toHaveValue('Acme Widgets (acme.example)');
      });

      fireEvent.click(screen.getByLabelText('Clear'));
      await waitFor(() => expect(screen.getByLabelText('Company (linked)')).toHaveValue(''));

      fireEvent.click(screen.getByText('Update Lead'));

      await waitFor(() => {
        expect(leadsApi.updateLead).toHaveBeenCalledWith(1, expect.objectContaining({ company_id: 0 }));
      });
    });
  });
});