import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { LeadAutocomplete } from './LeadAutocomplete';
import { leadsApi } from '@/api/endpoints';
import { createMockLead } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';

vi.mock('@/api/endpoints', () => ({
  leadsApi: {
    getLeads: vi.fn(),
    getLead: vi.fn(),
  },
}));

const ion = createMockLead({ id: 5, contact_name: 'Ion Ionescu', company_name: 'Bolt Robotics' });

describe('LeadAutocomplete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(leadsApi.getLeads).mockResolvedValue({ data: [ion], total: 1, page: 1, limit: 20, total_pages: 1 });
    vi.mocked(leadsApi.getLead).mockResolvedValue(ion);
  });

  it('renders a labelled input and does not query until opened', () => {
    render(<LeadAutocomplete value={null} onChange={vi.fn()} />);

    expect(screen.getByLabelText('Lead')).toBeInTheDocument();
    expect(leadsApi.getLeads).not.toHaveBeenCalled();
  });

  it('opens with one search page and reports the picked lead id', async () => {
    const onChange = vi.fn();
    render(<LeadAutocomplete value={null} onChange={onChange} />);

    await pickAutocompleteOption('Lead', 'Ion Ionescu (Bolt Robotics)');

    expect(leadsApi.getLeads).toHaveBeenCalledWith({ search: undefined, limit: 20 });
    expect(onChange).toHaveBeenCalledWith(5, ion);
  });

  it('searches as the user types', async () => {
    render(<LeadAutocomplete value={null} onChange={vi.fn()} />);

    const input = screen.getByLabelText('Lead');
    input.focus();
    fireEvent.change(input, { target: { value: 'ion' } });

    await waitFor(() => expect(leadsApi.getLeads).toHaveBeenCalledWith({ search: 'ion', limit: 20 }), {
      timeout: 3000,
    });
  });

  it('looks the lead up when only its id is known', async () => {
    render(<LeadAutocomplete value={5} onChange={vi.fn()} />);

    await waitFor(() => expect(leadsApi.getLead).toHaveBeenCalledWith(5));
    await waitFor(() => expect(screen.getByLabelText('Lead')).toHaveValue('Ion Ionescu (Bolt Robotics)'));
  });

  it('reports null when the selection is cleared', async () => {
    const onChange = vi.fn();
    render(<LeadAutocomplete value={5} initialLead={ion} onChange={onChange} />);

    fireEvent.click(screen.getByLabelText('Clear'));

    await waitFor(() => expect(onChange).toHaveBeenCalledWith(null, null));
  });
});
