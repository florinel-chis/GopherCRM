import React, { useCallback, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { usersApi, type UserFilters } from '@/api/endpoints';
import { RecordAutocomplete } from '@/components/RecordAutocomplete';
import type { User } from '@/types';

export interface StaffUserAutocompleteProps {
  label: string;
  value: number | null;
  onChange: (id: number | null) => void;
  error?: boolean;
  helperText?: string;
}

// GET /users filters by search only (it ignores is_active and role) and caps a
// page at 100, so eligibility is decided here and the search pages on until it
// has enough staff users or has read ten pages.
const PAGE_SIZE = 100;
const MAX_PAGES = 10;
const WANTED = 20;

const isAssignable = (user: User): boolean => user.is_active && user.role !== 'customer';

const userLabel = (user: User): string => {
  const name = `${user.first_name} ${user.last_name}`.trim() || user.email;
  if (!user.is_active) return `${name} (inactive)`;
  return `${name} (${user.role})`;
};

/** Picker of an active staff user (the users a rule step may name); the value is a user id. */
export const StaffUserAutocomplete: React.FC<StaffUserAutocompleteProps> = ({
  label,
  value,
  onChange,
  error,
  helperText,
}) => {
  const [stoppedEarly, setStoppedEarly] = useState(false);
  const [picked, setPicked] = useState<User | null>(null);

  const search = useCallback(async (term: string): Promise<User[]> => {
    const found: User[] = [];
    let more = false;
    for (let pageIndex = 0; pageIndex < MAX_PAGES; pageIndex += 1) {
      const offset = pageIndex * PAGE_SIZE;
      const filters: UserFilters = { search: term || undefined, offset, limit: PAGE_SIZE };
      const result = await usersApi.getUsers(filters);
      found.push(...result.data.filter(isAssignable));
      more = offset + PAGE_SIZE < result.total && result.data.length > 0;
      if (found.length >= WANTED || !more) break;
    }
    setStoppedEarly(more || found.length > WANTED);
    return found.slice(0, WANTED);
  }, []);

  // The selected account, to flag one that can no longer be assigned. Shares
  // its cache entry with the picker's own lookup.
  const { data: lookedUp } = useQuery({
    queryKey: ['user', value],
    queryFn: () => usersApi.getUser(value as number),
    enabled: value !== null && picked?.id !== value,
  });
  const selected = picked?.id === value ? picked : lookedUp?.id === value ? lookedUp : null;
  const unassignable = selected !== null && !isAssignable(selected);

  return (
    <RecordAutocomplete<User>
      value={value}
      onChange={(id, user) => {
        setPicked(user);
        onChange(id);
      }}
      label={label}
      queryKeys={['user', 'users-staff']}
      search={search}
      lookup={usersApi.getUser}
      getOptionLabel={userLabel}
      noOptionsText="No matching staff users"
      emptySearchText="Type to search users"
      error={error || unassignable}
      helperText={
        unassignable
          ? 'This user can no longer be assigned'
          : helperText ?? (stoppedEarly ? 'Type to narrow the list' : undefined)
      }
    />
  );
};
