import React, { useCallback, useMemo } from 'react';
import { dealsApi } from '@/api/endpoints';
import type { Deal, QuoteDealSummary } from '@/types';
import { dealAccountName } from '@/pages/deals/dealFormat';
import { RecordAutocomplete } from './RecordAutocomplete';

/** A full deal from the search or the lookup, or the summary a task carries. */
type DealOption = Deal | QuoteDealSummary;

export interface DealAutocompleteProps {
  value: number | null;
  /**
   * The picked id and, when the option is a full deal, the deal itself. An
   * option that is only a summary (a task's preloaded deal re-picked) reports
   * the id with a null deal.
   */
  onChange: (dealId: number | null, deal: Deal | null) => void;
  initialDeal?: DealOption | null;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
  /**
   * true (the default, the quote dialog): only open deals are searched, since
   * a quote on a won or lost deal is refused. false (the task form): every deal
   * the caller may list, won and lost included. The flag is part of the search
   * cache key, so the two pickers never serve each other's cached results.
   */
  openOnly?: boolean;
  testId?: string;
}

const SEARCH_LIMIT = 20;

const isFullDeal = (option: DealOption): option is Deal => 'owner_id' in option;

// "Title (account)", or the bare title without a linked account or for a
// summary, which carries no account.
const dealOptionLabel = (option: DealOption): string => {
  if (!isFullDeal(option)) {
    return option.title;
  }
  const account = dealAccountName(option);
  return account === '—' ? option.title : `${option.title} (${account})`;
};

/**
 * Picker over GET /deals?search=&limit=20, with open=true unless openOnly is
 * false; the value is a deal id.
 */
export const DealAutocomplete: React.FC<DealAutocompleteProps> = ({
  value,
  onChange,
  initialDeal = null,
  label = 'Deal',
  helperText,
  error,
  disabled,
  openOnly = true,
  testId = 'deal-autocomplete',
}) => {
  const search = useCallback(
    async (term: string): Promise<DealOption[]> => {
      const result = await dealsApi.getDeals(
        openOnly
          ? { search: term || undefined, open: true, limit: SEARCH_LIMIT }
          : { search: term || undefined, limit: SEARCH_LIMIT }
      );
      return result.deals;
    },
    [openOnly]
  );

  const searchKey = useMemo(() => [{ open: openOnly }] as const, [openOnly]);

  const handleChange = useCallback(
    (dealId: number | null, record: DealOption | null) => {
      onChange(dealId, record && isFullDeal(record) ? record : null);
    },
    [onChange]
  );

  return (
    <RecordAutocomplete<DealOption>
      testId={testId}
      value={value}
      onChange={handleChange}
      initialRecord={initialDeal}
      label={label}
      queryKeys={['deal', 'deals']}
      search={search}
      searchKey={searchKey}
      lookup={dealsApi.getDeal}
      getOptionLabel={dealOptionLabel}
      noOptionsText={openOnly ? 'No matching open deals' : 'No matching deals'}
      emptySearchText={openOnly ? 'Type to search open deals' : 'Type to search deals'}
      helperText={helperText}
      error={error}
      disabled={disabled}
    />
  );
};
