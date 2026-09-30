import React from 'react';
import { leadsApi } from '@/api/endpoints';
import type { Lead } from '@/types';
import { RecordAutocomplete } from './RecordAutocomplete';

export interface LeadAutocompleteProps {
  value: number | null;
  onChange: (leadId: number | null, lead: Lead | null) => void;
  initialLead?: Lead | null;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
}

const SEARCH_LIMIT = 20;

const leadOptionLabel = (lead: Lead): string => {
  const name = lead.contact_name || lead.email;
  return lead.company_name ? `${name} (${lead.company_name})` : name;
};

const searchLeads = async (term: string): Promise<Lead[]> => {
  const result = await leadsApi.getLeads({ search: term || undefined, limit: SEARCH_LIMIT });
  return result.data;
};

/** Picker over GET /leads?search=&limit=20; the value is a lead id. */
export const LeadAutocomplete: React.FC<LeadAutocompleteProps> = ({
  value,
  onChange,
  initialLead = null,
  label = 'Lead',
  helperText,
  error,
  disabled,
}) => (
  <RecordAutocomplete<Lead>
    testId="lead-autocomplete"
    value={value}
    onChange={onChange}
    initialRecord={initialLead}
    label={label}
    queryKeys={['lead', 'leads']}
    search={searchLeads}
    lookup={leadsApi.getLead}
    getOptionLabel={leadOptionLabel}
    noOptionsText="No matching leads"
    emptySearchText="Type to search leads"
    helperText={helperText}
    error={error}
    disabled={disabled}
  />
);
