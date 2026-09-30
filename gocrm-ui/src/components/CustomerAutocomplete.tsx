import React from 'react';
import { customersApi } from '@/api/endpoints';
import type { Customer } from '@/types';
import { RecordAutocomplete } from './RecordAutocomplete';

export interface CustomerAutocompleteProps {
  value: number | null;
  onChange: (customerId: number | null, customer: Customer | null) => void;
  initialCustomer?: Customer | null;
  label?: string;
  helperText?: string;
  error?: boolean;
  disabled?: boolean;
}

const SEARCH_LIMIT = 20;

const customerOptionLabel = (customer: Customer): string => {
  const name = customer.contact_name || customer.email;
  return customer.company_name ? `${name} (${customer.company_name})` : name;
};

const searchCustomers = async (term: string): Promise<Customer[]> => {
  const result = await customersApi.getCustomers({ search: term || undefined, limit: SEARCH_LIMIT });
  return result.data;
};

/** Picker over GET /customers?search=&limit=20; the value is a customer id. */
export const CustomerAutocomplete: React.FC<CustomerAutocompleteProps> = ({
  value,
  onChange,
  initialCustomer = null,
  label = 'Customer',
  helperText,
  error,
  disabled,
}) => (
  <RecordAutocomplete<Customer>
    testId="customer-autocomplete"
    value={value}
    onChange={onChange}
    initialRecord={initialCustomer}
    label={label}
    queryKeys={['customer', 'customers']}
    search={searchCustomers}
    lookup={customersApi.getCustomer}
    getOptionLabel={customerOptionLabel}
    noOptionsText="No matching customers"
    emptySearchText="Type to search customers"
    helperText={helperText}
    error={error}
    disabled={disabled}
  />
);
