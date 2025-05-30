import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { Component as TicketForm } from './TicketForm';
import { ticketsApi, customersApi, usersApi } from '@/api/endpoints';
import { createMockTicket, createMockCustomer, createMockUser } from '@/test/factories';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import userEvent from '@testing-library/user-event';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
    useSearchParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  ticketsApi: {
    getTicket: vi.fn(),
    createTicket: vi.fn(),
    updateTicket: vi.fn(),
  },
  customersApi: {
    getCustomers: vi.fn(),
  },
  usersApi: {
    getUsers: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
  }),
}));

describe('TicketForm', () => {
  const mockNavigate = vi.fn();
  const mockCustomers = [
    createMockCustomer({ id: 1, company_name: 'Acme Corp' }),
    createMockCustomer({ id: 2, company_name: 'Tech Solutions' }),
    createMockCustomer({ id: 3, company_name: 'Global Industries' }),
  ];
  const mockSupportAgents = [
    createMockUser({ id: 1, first_name: 'John', last_name: 'Doe', role: 'support' }),
    createMockUser({ id: 2, first_name: 'Jane', last_name: 'Smith', role: 'support' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    (useNavigate as any).mockReturnValue(mockNavigate);
    (useParams as any).mockReturnValue({});
    (useSearchParams as any).mockReturnValue([new URLSearchParams(), vi.fn()]);
    
    (customersApi.getCustomers as any).mockResolvedValue({
      data: mockCustomers,
      total: 3,
      page: 1,
      limit: 100,
      total_pages: 1,
    });
    
    (usersApi.getUsers as any).mockResolvedValue({
      data: mockSupportAgents,
      total: 2,
      page: 1,
      limit: 100,
      total_pages: 1,
    });
  });

  describe('Create Mode', () => {
    it('renders create form with default values', async () => {
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
        expect(screen.getByLabelText(/subject/i)).toHaveValue('');
        expect(screen.getByLabelText(/description/i)).toHaveValue('');
        expect(screen.getByLabelText(/priority/i)).toHaveTextContent('Medium');
        expect(screen.getByLabelText(/status/i)).toHaveTextContent('Open');
      });
    });

    it('validates required fields', async () => {
      const user = userEvent.setup();
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Try to submit without filling required fields
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      // Check that the form hasn't submitted (createTicket not called)
      expect(ticketsApi.createTicket).not.toHaveBeenCalled();
      
      // Check for error states on inputs
      await waitFor(() => {
        const subjectInput = screen.getByLabelText(/subject/i);
        const descriptionInput = screen.getByLabelText(/description/i);
        
        // Check that we're still on the form
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
        expect(mockNavigate).not.toHaveBeenCalled();
      });
    });

    it('creates ticket with valid data', async () => {
      const user = userEvent.setup();
      (ticketsApi.createTicket as any).mockResolvedValue(
        createMockTicket({ id: 1, subject: 'New ticket' })
      );

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Fill in the form
      await user.type(screen.getByLabelText(/subject/i), 'System error');
      await user.type(screen.getByLabelText(/description/i), 'The system is showing an error message');
      
      // Select priority
      const prioritySelect = screen.getByLabelText(/priority/i);
      await user.click(prioritySelect);
      await user.click(screen.getByRole('option', { name: 'High' }));
      
      // Select customer
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.createTicket).toHaveBeenCalledWith({
          subject: 'System error',
          description: 'The system is showing an error message',
          status: 'open',
          priority: 'high',
          customer_id: 1,
          assigned_to_id: undefined,
        });
        expect(mockNavigate).toHaveBeenCalledWith('/tickets');
      });
    });

    it('assigns ticket to support agent', async () => {
      const user = userEvent.setup();
      (ticketsApi.createTicket as any).mockResolvedValue(
        createMockTicket({ id: 1, subject: 'New ticket' })
      );

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Fill required fields
      await user.type(screen.getByLabelText(/subject/i), 'System error');
      await user.type(screen.getByLabelText(/description/i), 'The system is showing an error message');
      
      // Select customer
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));
      
      // Select support agent
      const agentAutocomplete = screen.getByLabelText(/assign to support agent/i);
      await user.click(agentAutocomplete);
      await user.click(screen.getByText('John Doe'));
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.createTicket).toHaveBeenCalledWith(
          expect.objectContaining({
            assigned_to_id: 1,
          })
        );
      });
    });

    it('pre-selects customer when provided in URL', async () => {
      (useSearchParams as any).mockReturnValue([
        new URLSearchParams('customer_id=2'),
        vi.fn(),
      ]);

      render(<TicketForm />);

      await waitFor(() => {
        const customerInput = screen.getByLabelText(/customer/i);
        expect(customerInput).toHaveValue('Tech Solutions');
        expect(customerInput).toBeDisabled();
      });
    });

    it('cancels and navigates back', async () => {
      const user = userEvent.setup();
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      const cancelButton = screen.getByRole('button', { name: /cancel/i });
      await user.click(cancelButton);

      expect(mockNavigate).toHaveBeenCalledWith('/tickets');
    });
  });

  describe('Edit Mode', () => {
    const mockTicket = createMockTicket({
      id: 1,
      subject: 'Existing ticket',
      description: 'This is an existing ticket',
      status: 'in_progress',
      priority: 'high',
      customer_id: 2,
      assigned_to_id: 1,
      customer: mockCustomers[1],
      assigned_to: mockSupportAgents[0],
    });

    beforeEach(() => {
      (useParams as any).mockReturnValue({ id: '1' });
      (ticketsApi.getTicket as any).mockResolvedValue(mockTicket);
    });

    it('renders edit form with existing data', async () => {
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
        expect(screen.getByLabelText(/subject/i)).toHaveValue('Existing ticket');
        expect(screen.getByLabelText(/description/i)).toHaveValue('This is an existing ticket');
        expect(screen.getByLabelText(/priority/i)).toHaveTextContent('High');
        expect(screen.getByLabelText(/status/i)).toHaveTextContent('In Progress');
        expect(screen.getByLabelText(/customer/i)).toHaveValue('Tech Solutions');
        expect(screen.getByLabelText(/assign to support agent/i)).toHaveValue('John Doe');
      });
    });

    it('updates ticket with changed data', async () => {
      const user = userEvent.setup();
      (ticketsApi.updateTicket as any).mockResolvedValue(mockTicket);

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
      });

      // Clear and update subject
      const subjectInput = screen.getByLabelText(/subject/i);
      await user.clear(subjectInput);
      await user.type(subjectInput, 'Updated ticket subject');
      
      // Change status
      const statusSelect = screen.getByLabelText(/status/i);
      await user.click(statusSelect);
      await user.click(screen.getByRole('option', { name: 'Resolved' }));
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /update ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.updateTicket).toHaveBeenCalledWith(1, {
          subject: 'Updated ticket subject',
          description: 'This is an existing ticket',
          status: 'resolved',
          priority: 'high',
          customer_id: 2,
          assigned_to_id: 1,
        });
        expect(mockNavigate).toHaveBeenCalledWith('/tickets');
      });
    });

    it('shows loading state while fetching ticket', () => {
      (ticketsApi.getTicket as any).mockImplementation(() => new Promise(() => {}));
      
      render(<TicketForm />);

      expect(screen.getByTestId('loading')).toBeInTheDocument();
    });

    it('changes assigned agent', async () => {
      const user = userEvent.setup();
      (ticketsApi.updateTicket as any).mockResolvedValue(mockTicket);

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
      });

      // Clear current agent and select new one
      const agentAutocomplete = screen.getByLabelText(/assign to support agent/i);
      const clearButton = agentAutocomplete.parentElement?.querySelector('[title="Clear"]');
      if (clearButton) {
        await user.click(clearButton);
      }
      
      await user.click(agentAutocomplete);
      await user.click(screen.getByText('Jane Smith'));
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /update ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.updateTicket).toHaveBeenCalledWith(1, 
          expect.objectContaining({
            assigned_to_id: 2,
          })
        );
      });
    });

    it('unassigns ticket by clearing agent', async () => {
      const user = userEvent.setup();
      (ticketsApi.updateTicket as any).mockResolvedValue(mockTicket);

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
      });

      // Clear current agent
      const agentAutocomplete = screen.getByLabelText(/assign to support agent/i);
      const clearButton = agentAutocomplete.parentElement?.querySelector('[title="Clear"]');
      expect(clearButton).toBeInTheDocument();
      await user.click(clearButton!);
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /update ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.updateTicket).toHaveBeenCalledWith(1, 
          expect.objectContaining({
            assigned_to_id: undefined,
          })
        );
      });
    });
  });

  describe('Form Validation', () => {
    it('validates subject length', async () => {
      const user = userEvent.setup();
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Clear subject and try to submit
      const subjectInput = screen.getByLabelText(/subject/i);
      await user.type(subjectInput, 'a');
      await user.clear(subjectInput);
      
      // Fill other required fields
      await user.type(screen.getByLabelText(/description/i), 'Test description');
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));
      
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      // Form should not submit
      await waitFor(() => {
        expect(ticketsApi.createTicket).not.toHaveBeenCalled();
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });
    });

    it('validates description is not empty', async () => {
      const user = userEvent.setup();
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Fill other required fields
      await user.type(screen.getByLabelText(/subject/i), 'Test subject');
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));
      
      // Type and clear description
      const descriptionInput = screen.getByLabelText(/description/i);
      await user.type(descriptionInput, 'test');
      await user.clear(descriptionInput);
      
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      // Form should not submit
      await waitFor(() => {
        expect(ticketsApi.createTicket).not.toHaveBeenCalled();
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });
    });
  });

  describe('API Error Handling', () => {
    it('handles create ticket API error', async () => {
      const user = userEvent.setup();
      (ticketsApi.createTicket as any).mockRejectedValue(new Error('API Error'));

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Fill in the form
      await user.type(screen.getByLabelText(/subject/i), 'Test ticket');
      await user.type(screen.getByLabelText(/description/i), 'Test description');
      
      // Select customer
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));
      
      // Submit form
      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.createTicket).toHaveBeenCalled();
        // Form should still be visible after error
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
        expect(mockNavigate).not.toHaveBeenCalled();
      });
    });

    it('handles update ticket API error', async () => {
      const user = userEvent.setup();
      (useParams as any).mockReturnValue({ id: '1' });
      (ticketsApi.getTicket as any).mockResolvedValue(
        createMockTicket({ id: 1, subject: 'Test ticket' })
      );
      (ticketsApi.updateTicket as any).mockRejectedValue(new Error('API Error'));

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
      });

      // Make a change and submit
      const subjectInput = screen.getByLabelText(/subject/i);
      await user.clear(subjectInput);
      await user.type(subjectInput, 'Updated subject');
      
      const submitButton = screen.getByRole('button', { name: /update ticket/i });
      await user.click(submitButton);

      await waitFor(() => {
        expect(ticketsApi.updateTicket).toHaveBeenCalled();
        // Form should still be visible after error
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
        expect(mockNavigate).not.toHaveBeenCalled();
      });
    });
  });

  describe('Form Interactions', () => {
    it('disables submit button while processing', async () => {
      const user = userEvent.setup();
      let resolveCreate: any;
      (ticketsApi.createTicket as any).mockImplementation(
        () => new Promise((resolve) => { resolveCreate = resolve; })
      );

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      // Fill required fields
      await user.type(screen.getByLabelText(/subject/i), 'Test');
      await user.type(screen.getByLabelText(/description/i), 'Test');
      
      const customerAutocomplete = screen.getByLabelText(/customer/i);
      await user.click(customerAutocomplete);
      await user.click(screen.getByText('Acme Corp'));

      const submitButton = screen.getByRole('button', { name: /create ticket/i });
      await user.click(submitButton);

      // Button should be disabled while processing
      expect(submitButton).toBeDisabled();

      // Resolve the promise
      resolveCreate(createMockTicket({ id: 1 }));

      await waitFor(() => {
        expect(mockNavigate).toHaveBeenCalledWith('/tickets');
      });
    });

    it('allows selecting all priority levels', async () => {
      const user = userEvent.setup();
      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Create New Ticket')).toBeInTheDocument();
      });

      const prioritySelect = screen.getByLabelText(/priority/i);
      
      // Test Low
      await user.click(prioritySelect);
      await user.click(screen.getByRole('option', { name: 'Low' }));
      expect(prioritySelect).toHaveTextContent('Low');
      
      // Test Medium
      await user.click(prioritySelect);
      await user.click(screen.getByRole('option', { name: 'Medium' }));
      expect(prioritySelect).toHaveTextContent('Medium');
      
      // Test High
      await user.click(prioritySelect);
      await user.click(screen.getByRole('option', { name: 'High' }));
      expect(prioritySelect).toHaveTextContent('High');
      
      // Test Urgent
      await user.click(prioritySelect);
      await user.click(screen.getByRole('option', { name: 'Urgent' }));
      expect(prioritySelect).toHaveTextContent('Urgent');
    });

    it('allows selecting all status values in edit mode', async () => {
      const user = userEvent.setup();
      (useParams as any).mockReturnValue({ id: '1' });
      (ticketsApi.getTicket as any).mockResolvedValue(
        createMockTicket({ id: 1, status: 'open' })
      );

      render(<TicketForm />);

      await waitFor(() => {
        expect(screen.getByText('Edit Ticket')).toBeInTheDocument();
      });

      const statusSelect = screen.getByLabelText(/status/i);
      
      // Test all status options
      const statuses = ['Open', 'In Progress', 'Resolved', 'Closed'];
      for (const status of statuses) {
        await user.click(statusSelect);
        await user.click(screen.getByRole('option', { name: status }));
        expect(statusSelect).toHaveTextContent(status);
      }
    });
  });
});