import React from 'react';
import { dealsApi } from '@/api/endpoints';
import type { Deal } from '@/types';
import { dealAccountName } from '@/pages/deals/dealFormat';
import { RecordAutocomplete } from './RecordAutocomplete';

export interface DealAutocompleteProps {
  value: number | null;
  onChange: (dealId: number | null, deal: Deal | null) => void;
  initialDeal?: Deal | null;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
}

const SEARCH_LIMIT = 20;

// "Title (account)", or the bare title without a linked account.
const dealOptionLabel = (deal: Deal): string => {
  const account = dealAccountName(deal);
  return account === '—' ? deal.title : `${deal.title} (${account})`;
};

// Only open deals are offered: a quote on a won or lost deal is refused by
// the API with a 409.
const searchDeals = async (term: string): Promise<Deal[]> => {
  const result = await dealsApi.getDeals({ search: term || undefined, open: true, limit: SEARCH_LIMIT });
  return result.deals;
};

/** Picker over GET /deals?open=true&search=&limit=20; the value is a deal id. */
export const DealAutocomplete: React.FC<DealAutocompleteProps> = ({
  value,
  onChange,
  initialDeal = null,
  label = 'Deal',
  helperText,
  error,
  disabled,
}) => (
  <RecordAutocomplete<Deal>
    testId="deal-autocomplete"
    value={value}
    onChange={onChange}
    initialRecord={initialDeal}
    label={label}
    queryKeys={['deal', 'deals']}
    search={searchDeals}
    lookup={dealsApi.getDeal}
    getOptionLabel={dealOptionLabel}
    noOptionsText="No matching open deals"
    emptySearchText="Type to search open deals"
    helperText={helperText}
    error={error}
    disabled={disabled}
  />
);
