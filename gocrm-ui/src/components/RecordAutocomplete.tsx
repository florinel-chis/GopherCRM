import React, { useEffect, useMemo, useState } from 'react';
import { Autocomplete, CircularProgress, TextField } from '@mui/material';
import { useQuery } from '@tanstack/react-query';

export interface RecordAutocompleteProps<T extends { id: number }> {
  /** Selected record id, or null for "nothing linked". */
  value: number | null;
  onChange: (id: number | null, record: T | null) => void;
  /**
   * The record already known to the caller (a deal's preloaded customer or
   * lead). Saves a round trip when `value` matches its id; when `value` names
   * another id the component fetches that record instead.
   */
  initialRecord?: T | null;
  label: string;
  /** Singular and plural query-key roots, e.g. ['customer', 'customers']. */
  queryKeys: [string, string];
  /** Server-side search, one page of matches for the typed text. */
  search: (term: string) => Promise<T[]>;
  /** Fetch by id, for a value that arrives without its record. */
  lookup: (id: number) => Promise<T>;
  getOptionLabel: (record: T) => string;
  noOptionsText: string;
  emptySearchText: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
  testId?: string;
}

const SEARCH_DEBOUNCE_MS = 300;

/**
 * Search-as-you-type picker over a server-side list, generic over the record.
 *
 * Same behaviour as CompanyAutocomplete: the value is an id (or null); options
 * are fetched server-side, so the client-side filter is disabled; the request
 * only fires while the list is open, and only after the typed text has
 * settled. CustomerAutocomplete and LeadAutocomplete are thin wrappers.
 */
export function RecordAutocomplete<T extends { id: number }>({
  value,
  onChange,
  initialRecord = null,
  label,
  queryKeys,
  search,
  lookup,
  getOptionLabel,
  noOptionsText,
  emptySearchText,
  helperText,
  error,
  disabled,
  testId,
}: RecordAutocompleteProps<T>): React.ReactElement {
  const [singularKey, pluralKey] = queryKeys;
  const [open, setOpen] = useState(false);
  const [inputValue, setInputValue] = useState('');
  const [searchTerm, setSearchTerm] = useState('');
  const [selected, setSelected] = useState<T | null>(
    initialRecord && initialRecord.id === value ? initialRecord : null
  );

  useEffect(() => {
    const timer = setTimeout(() => setSearchTerm(inputValue.trim()), SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [inputValue]);

  // Keep the selected record in step with the controlled id: a parent that
  // resets the form (edit mode loading its record, a query-string prefill)
  // changes `value` without going through onChange. Deliberately not keyed on
  // `selected`: a pick must survive until the parent's next `value`.
  useEffect(() => {
    if (value === null) {
      setSelected(null);
      return;
    }
    if (initialRecord && initialRecord.id === value) {
      setSelected(initialRecord);
    }
  }, [value, initialRecord]);

  const needsLookup = value !== null && selected?.id !== value && initialRecord?.id !== value;

  const { data: lookedUp } = useQuery({
    queryKey: [singularKey, value],
    queryFn: () => lookup(value as number),
    enabled: needsLookup,
  });

  useEffect(() => {
    if (lookedUp && lookedUp.id === value) {
      setSelected(lookedUp);
    }
  }, [lookedUp, value]);

  const { data: searchResult, isFetching } = useQuery({
    queryKey: [pluralKey, 'search', searchTerm],
    queryFn: () => search(searchTerm),
    enabled: open,
  });

  // The selected record has to be in the option list or MUI reports the
  // value as unknown; it is prepended when the search page does not carry it.
  const options = useMemo(() => {
    const fetched = searchResult ?? [];
    if (selected && !fetched.some((record) => record.id === selected.id)) {
      return [selected, ...fetched];
    }
    return fetched;
  }, [searchResult, selected]);

  return (
    <Autocomplete<T, false, false, false>
      data-testid={testId}
      open={open}
      onOpen={() => setOpen(true)}
      onClose={() => setOpen(false)}
      value={selected}
      onChange={(_, record) => {
        setSelected(record);
        onChange(record ? record.id : null, record);
      }}
      inputValue={inputValue}
      onInputChange={(_, text) => setInputValue(text)}
      options={options}
      filterOptions={(candidates) => candidates}
      getOptionLabel={getOptionLabel}
      isOptionEqualToValue={(option, current) => option.id === current.id}
      loading={open && isFetching}
      disabled={disabled}
      noOptionsText={searchTerm ? noOptionsText : emptySearchText}
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
}
