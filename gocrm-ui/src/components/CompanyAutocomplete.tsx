import React, { useEffect, useMemo, useState } from 'react';
import { Autocomplete, CircularProgress, TextField } from '@mui/material';
import { useQuery } from '@tanstack/react-query';
import { companiesApi } from '@/api/endpoints';
import type { Company } from '@/types';

export interface CompanyAutocompleteProps {
  /** Selected company id, or null for "no linked company". */
  value: number | null;
  onChange: (companyId: number | null, company: Company | null) => void;
  /**
   * The company record already known to the caller (a lead's or customer's
   * `company_record`). Saves a round trip when `value` matches its id; when
   * `value` names another id the component fetches that company instead.
   */
  initialCompany?: Company | null;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
}

const SEARCH_DEBOUNCE_MS = 300;
const SEARCH_LIMIT = 20;

const companyOptionLabel = (company: Company): string =>
  company.domain ? `${company.name} (${company.domain})` : company.name;

/**
 * Search-as-you-type picker over GET /companies?search=&limit=20.
 *
 * The value is a company id (or null), which is what the lead and customer
 * forms send as `company_id`. Options are fetched server-side, so the
 * client-side filter is disabled; the request only fires while the list is
 * open, and only after the typed text has settled.
 */
export const CompanyAutocomplete: React.FC<CompanyAutocompleteProps> = ({
  value,
  onChange,
  initialCompany = null,
  label = 'Company (linked)',
  helperText,
  error,
  disabled,
}) => {
  const [open, setOpen] = useState(false);
  const [inputValue, setInputValue] = useState('');
  const [searchTerm, setSearchTerm] = useState('');
  const [selected, setSelected] = useState<Company | null>(
    initialCompany && initialCompany.id === value ? initialCompany : null
  );

  useEffect(() => {
    const timer = setTimeout(() => setSearchTerm(inputValue.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [inputValue]);

  // Keep the selected record in step with the controlled id: a parent that
  // resets the form (edit mode loading its record, a `?company_id=` prefill)
  // changes `value` without going through onChange. Deliberately not keyed on
  // `selected`: a pick must survive until the parent's next `value`.
  useEffect(() => {
    if (value === null) {
      setSelected(null);
      return;
    }
    if (initialCompany && initialCompany.id === value) {
      setSelected(initialCompany);
    }
  }, [value, initialCompany]);

  const needsLookup = value !== null && selected?.id !== value && initialCompany?.id !== value;

  const { data: lookedUp } = useQuery({
    queryKey: ['company', value],
    queryFn: () => companiesApi.getCompany(value as number),
    enabled: needsLookup,
  });

  useEffect(() => {
    if (lookedUp && lookedUp.id === value) {
      setSelected(lookedUp);
    }
  }, [lookedUp, value]);

  const { data: searchResult, isFetching } = useQuery({
    queryKey: ['companies', 'search', searchTerm],
    queryFn: () => companiesApi.getCompanies({ search: searchTerm, limit: SEARCH_LIMIT }),
    enabled: open,
  });

  // The selected company has to be in the option list or MUI reports the
  // value as unknown; it is prepended when the search page does not carry it.
  const options = useMemo(() => {
    const fetched = searchResult?.companies ?? [];
    if (selected && !fetched.some((company) => company.id === selected.id)) {
      return [selected, ...fetched];
    }
    return fetched;
  }, [searchResult, selected]);

  return (
    <Autocomplete<Company, false, false, false>
      data-testid="company-autocomplete"
      open={open}
      onOpen={() => setOpen(true)}
      onClose={() => setOpen(false)}
      value={selected}
      onChange={(_, company) => {
        setSelected(company);
        onChange(company ? company.id : null, company);
      }}
      inputValue={inputValue}
      onInputChange={(_, text) => setInputValue(text)}
      options={options}
      filterOptions={(candidates) => candidates}
      getOptionLabel={companyOptionLabel}
      isOptionEqualToValue={(option, current) => option.id === current.id}
      loading={open && isFetching}
      disabled={disabled}
      noOptionsText={searchTerm ? 'No matching companies' : 'Type to search companies'}
      renderInput={(params) => (
        <TextField
          {...params}
          label={label}
          error={error}
          helperText={helperText}
          InputProps={{
            ...params.InputProps,
            endAdornment: (
              <>
                {open && isFetching ? <CircularProgress color="inherit" size={18} /> : null}
                {params.InputProps.endAdornment}
              </>
            ),
          }}
        />
      )}
    />
  );
};
