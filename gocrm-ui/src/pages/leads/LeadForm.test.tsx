import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as LeadForm } from './LeadForm';
import { leadsApi } from '@/api/endpoints';
import { createMockLead } from '@/test/factories';
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
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
  }),
}));

describe('LeadForm', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (useParams as any).mockReturnValue({});
  });

  describe('Create Mode', () => {
    it('renders create form', () => {
      render(<LeadForm />);

      expect(screen.getByText('Create New Lead')).toBeInTheDocument();
      expect(screen.getByLabelText(/Company Name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Contact Name/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Email/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Phone/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Status/i)).toBeInTheDocument();
      expect(screen.getByLabelText(/Lead Source/i)).toBeInTheDocument();
    });

    it('submits form with valid data', async () => {
      (leadsApi.createLead as any).mockResolvedValue(createMockLead());
      
      render(<LeadForm />);

      fireEvent.change(screen.getByLabelText(/Company Name/i), {
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
      fireEvent.change(screen.getByLabelText(/Company Name/i), {
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
      (useParams as any).mockReturnValue({ id: '1' });
      (leadsApi.getLead as any).mockResolvedValue(mockLead);
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
      (leadsApi.updateLead as any).mockResolvedValue(mockLead);
      
      render(<LeadForm />);

      await waitFor(() => {
        expect(screen.getByDisplayValue('Existing Company')).toBeInTheDocument();
      });

      // Update company name
      const companyInput = screen.getByLabelText(/Company Name/i);
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
  });
});