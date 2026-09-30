import React, { useEffect, useState } from 'react';
import { useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { useForm, FormProvider, Controller } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Box,
  Paper,
  Typography,
  Button,
  Stack,
  Divider,
  Autocomplete,
  FormControl,
  FormHelperText,
  InputLabel,
  MenuItem,
  Select,
  TextField as MuiTextField,
} from '@mui/material';
import { Save as SaveIcon, Cancel as CancelIcon } from '@mui/icons-material';
import { AxiosError } from 'axios';
import { FormTextField } from '@/components/form';
import { CompanyAutocomplete } from '@/components/CompanyAutocomplete';
import { CustomerAutocomplete } from '@/components/CustomerAutocomplete';
import { LeadAutocomplete } from '@/components/LeadAutocomplete';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { configurationsApi, dealsApi, usersApi, type CreateDealData } from '@/api/endpoints';
import {
  DEAL_STAGES,
  DEAL_STAGE_VALUES,
  dealStageDefaultProbability,
  type Company,
  type Customer,
  type Lead,
  type User,
} from '@/types';
import { centsToDecimal, decimalToCents } from './dealFormat';

const FALLBACK_CURRENCY = 'EUR';
const DEFAULT_CURRENCY_KEY = 'deals.default_currency';
const PROBABILITY_MESSAGE = 'Probability must be a whole number between 0 and 100';

// Bounds mirror the API's column sizes (title 200, lost_reason 255, source
// 100); the amount is edited as a decimal and sent as integer cents.
const dealSchema = z.object({
  title: z
    .string()
    .trim()
    .min(1, 'Title is required')
    .max(200, 'Title must be 200 characters or fewer'),
  stage: z.enum(DEAL_STAGE_VALUES),
  amount: z
    .string()
    .trim()
    .refine((value) => value === '' || /^\d+(\.\d{1,2})?$/.test(value), {
      message: 'Amount must be a positive number with at most two decimals',
    }),
  currency: z
    .string()
    .trim()
    .toUpperCase()
    .regex(/^[A-Z]{3}$/, 'Currency must be a 3-letter ISO code'),
  probability: z
    .number({ invalid_type_error: PROBABILITY_MESSAGE })
    .int(PROBABILITY_MESSAGE)
    .min(0, PROBABILITY_MESSAGE)
    .max(100, PROBABILITY_MESSAGE),
  expected_close_date: z
    .string()
    .refine((value) => value === '' || /^\d{4}-\d{2}-\d{2}$/.test(value), {
      message: 'Expected close must be a date',
    }),
  lost_reason: z.string().trim().max(255, 'Lost reason must be 255 characters or fewer'),
  source: z.string().trim().max(100, 'Source must be 100 characters or fewer'),
  notes: z.string(),
});

type DealFormData = z.infer<typeof dealSchema>;

// What the axios client leaves in `error.response.data`: the `error` object of
// the API envelope ({code, message, details}), not the envelope itself.
interface ApiErrorPayload {
  code?: string;
  message?: string;
}

type ReferenceField = 'company_id' | 'customer_id' | 'lead_id' | 'owner_id';
const REFERENCE_FIELDS: ReferenceField[] = ['company_id', 'customer_id', 'lead_id', 'owner_id'];

const emptyValues: DealFormData = {
  title: '',
  stage: 'qualification',
  amount: '',
  currency: FALLBACK_CURRENCY,
  probability: dealStageDefaultProbability('qualification'),
  expected_close_date: '',
  lost_reason: '',
  source: '',
  notes: '',
};

const positiveId = (raw: string | null): number | null => {
  const parsed = raw ? Number(raw) : NaN;
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
};

const MISSING_LINK_TEXT = 'Linked record no longer available; pick another to replace it';

// What a loaded deal seeds a picker with. A link whose record came preloaded
// is shown as usual. An id without its record means the record was erased
// (the API preloads live rows only): the picker stays empty with a null
// original, so an untouched picker sends nothing and the API keeps the link,
// and a picked record replaces it.
const loadedLink = <T extends { id: number }>(
  linkId: number | undefined,
  record: T | undefined
): { id: number | null; record: T | null; missing: boolean } => {
  if (record && record.id === linkId) {
    return { id: linkId, record, missing: false };
  }
  return { id: null, record: null, missing: linkId !== undefined };
};

// Chosen → send it; cleared on edit after being linked → 0 clears it on the
// API; never linked and untouched → omit so the API keeps whatever it has.
const referencePatch = (
  field: ReferenceField,
  current: number | null,
  original: number | null,
  isEditMode: boolean
): Partial<CreateDealData> => {
  if (current !== null) {
    return { [field]: current };
  }
  if (isEditMode && original !== null) {
    return { [field]: 0 };
  }
  return {};
};

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const [searchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const isEditMode = !!id;
  const isAdmin = user?.role === 'admin';

  // "New Deal" on a company or customer page arrives with the id in the query.
  const prefilledCompanyId = positiveId(searchParams.get('company_id'));
  const prefilledCustomerId = positiveId(searchParams.get('customer_id'));

  const [companyId, setCompanyId] = useState<number | null>(isEditMode ? null : prefilledCompanyId);
  const [company, setCompany] = useState<Company | null>(null);
  const [originalCompanyId, setOriginalCompanyId] = useState<number | null>(null);

  const [customerId, setCustomerId] = useState<number | null>(isEditMode ? null : prefilledCustomerId);
  const [customer, setCustomer] = useState<Customer | null>(null);
  const [originalCustomerId, setOriginalCustomerId] = useState<number | null>(null);

  const [leadId, setLeadId] = useState<number | null>(null);
  const [lead, setLead] = useState<Lead | null>(null);
  const [originalLeadId, setOriginalLeadId] = useState<number | null>(null);

  // Only an admin may hand a deal to another user; sales never sends
  // owner_id and the API assigns the caller. An untouched picker is not sent,
  // so create defaults to the caller and update keeps the current owner.
  const [owner, setOwner] = useState<User | null>(null);
  const [ownerTouched, setOwnerTouched] = useState(false);

  const [referenceErrors, setReferenceErrors] = useState<Partial<Record<ReferenceField, string>>>({});
  const [missingLinks, setMissingLinks] = useState<Partial<Record<ReferenceField, boolean>>>({});

  // The probability follows the stage default until the user edits it; the
  // currency follows the configured default until the user edits it.
  const [probabilityTouched, setProbabilityTouched] = useState(false);
  const [currencyTouched, setCurrencyTouched] = useState(false);

  const methods = useForm<DealFormData>({
    resolver: zodResolver(dealSchema),
    defaultValues: emptyValues,
  });
  const { control, setValue } = methods;

  const { data: deal, isLoading } = useQuery({
    queryKey: ['deal', id],
    queryFn: () => dealsApi.getDeal(Number(id)),
    enabled: isEditMode,
  });

  const { data: usersData } = useQuery({
    queryKey: ['users', { is_active: true }],
    queryFn: () => usersApi.getUsers({ is_active: true }),
    enabled: isAdmin,
  });

  // `deals.default_currency` is read from the UI-safe configuration list the
  // app already loads for every authenticated user; EUR when absent.
  const { data: uiConfigurations } = useQuery({
    queryKey: ['configurations', 'ui'],
    queryFn: () => configurationsApi.getUIConfigurations(),
    enabled: !isEditMode,
  });

  useEffect(() => {
    if (isEditMode || currencyTouched || !uiConfigurations) {
      return;
    }
    const configured = uiConfigurations.find((config) => config.key === DEFAULT_CURRENCY_KEY)?.value;
    if (typeof configured === 'string' && /^[A-Za-z]{3}$/.test(configured.trim())) {
      setValue('currency', configured.trim().toUpperCase());
    }
  }, [uiConfigurations, isEditMode, currencyTouched, setValue]);

  useEffect(() => {
    if (deal) {
      methods.reset({
        title: deal.title,
        stage: deal.stage,
        amount: centsToDecimal(deal.amount_cents),
        currency: deal.currency,
        probability: deal.probability,
        expected_close_date: deal.expected_close_date ?? '',
        lost_reason: deal.lost_reason || '',
        source: deal.source || '',
        notes: deal.notes || '',
      });
      const companyLink = loadedLink(deal.company_id, deal.company);
      setCompanyId(companyLink.id);
      setCompany(companyLink.record);
      setOriginalCompanyId(companyLink.id);
      const customerLink = loadedLink(deal.customer_id, deal.customer);
      setCustomerId(customerLink.id);
      setCustomer(customerLink.record);
      setOriginalCustomerId(customerLink.id);
      const leadLink = loadedLink(deal.lead_id, deal.lead);
      setLeadId(leadLink.id);
      setLead(leadLink.record);
      setOriginalLeadId(leadLink.id);
      setMissingLinks({
        company_id: companyLink.missing,
        customer_id: customerLink.missing,
        lead_id: leadLink.missing,
      });
      setOwner(deal.owner ?? null);
    }
  }, [deal, methods]);

  // An unknown or deleted reference comes back as 400 INVALID_REFERENCE with
  // no details and the field named in the message ("unknown company_id 7:
  // company not found"); the message belongs on that picker. A 403 is the
  // owner rule (sales sending another owner_id). Anything else that carries a
  // message is shown as is.
  const reportError = (error: unknown, fallback: string) => {
    if (error instanceof AxiosError) {
      const payload = error.response?.data as ApiErrorPayload | undefined;
      const serverMessage =
        typeof payload?.message === 'string' && payload.message !== '' ? payload.message : undefined;
      if (payload?.code === 'INVALID_REFERENCE' && serverMessage) {
        const field = REFERENCE_FIELDS.find((candidate) => serverMessage.includes(candidate));
        if (field) {
          setReferenceErrors((prev) => ({ ...prev, [field]: serverMessage }));
        }
        showError(serverMessage);
        return;
      }
      if (serverMessage) {
        showError(serverMessage);
        return;
      }
    }
    showError(fallback);
  };

  const invalidateDeals = () => {
    queryClient.invalidateQueries({ queryKey: ['deals'] });
    queryClient.invalidateQueries({ queryKey: ['company'] });
    queryClient.invalidateQueries({ queryKey: ['customer'] });
  };

  const createMutation = useMutation({
    mutationFn: (data: CreateDealData) => dealsApi.createDeal(data),
    onSuccess: (created) => {
      showSuccess('Deal created successfully');
      invalidateDeals();
      navigate(`/deals/${created.id}`);
    },
    onError: (error) => reportError(error, 'Failed to create deal'),
  });

  const updateMutation = useMutation({
    mutationFn: (data: CreateDealData) => dealsApi.updateDeal(Number(id), data),
    onSuccess: () => {
      showSuccess('Deal updated successfully');
      invalidateDeals();
      queryClient.invalidateQueries({ queryKey: ['deal', id] });
      navigate(`/deals/${id}`);
    },
    onError: (error) => reportError(error, 'Failed to update deal'),
  });

  const clearReferenceError = (field: ReferenceField) =>
    setReferenceErrors((prev) => {
      if (!prev[field]) {
        return prev;
      }
      const next = { ...prev };
      delete next[field];
      return next;
    });

  const ownerPatch = (): Pick<CreateDealData, 'owner_id'> =>
    isAdmin && ownerTouched && owner ? { owner_id: owner.id } : {};

  // The picker's helper line: a server rejection first, then the note about
  // an erased record while nothing has been picked in its place.
  const linkHelperText = (field: ReferenceField, current: number | null): string =>
    referenceErrors[field] ?? (missingLinks[field] && current === null ? MISSING_LINK_TEXT : 'Optional');

  const onSubmit = (data: DealFormData) => {
    const payload: CreateDealData = {
      title: data.title,
      stage: data.stage,
      amount_cents: decimalToCents(data.amount),
      currency: data.currency,
      probability: data.probability,
      expected_close_date: data.expected_close_date || null,
      lost_reason: data.stage === 'lost' ? data.lost_reason : '',
      source: data.source,
      notes: data.notes,
      ...referencePatch('company_id', companyId, originalCompanyId, isEditMode),
      ...referencePatch('customer_id', customerId, originalCustomerId, isEditMode),
      ...referencePatch('lead_id', leadId, originalLeadId, isEditMode),
      ...ownerPatch(),
    };
    if (isEditMode) {
      updateMutation.mutate(payload);
    } else {
      createMutation.mutate(payload);
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  const users = usersData?.data || [];
  const stageValue = methods.watch('stage');

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">{isEditMode ? 'Edit Deal' : 'Create New Deal'}</Typography>
      </Box>

      <Paper sx={{ p: 3 }}>
        <FormProvider {...methods}>
          <form onSubmit={methods.handleSubmit(onSubmit)} noValidate>
            <Stack spacing={3}>
              <Typography variant="h6">Deal</Typography>

              <FormTextField name="title" label="Title" required />

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
                <Controller
                  name="stage"
                  control={control}
                  render={({ field, fieldState: { error } }) => (
                    <FormControl fullWidth error={!!error}>
                      <InputLabel id="stage-label">Stage</InputLabel>
                      <Select
                        {...field}
                        labelId="stage-label"
                        id="stage"
                        label="Stage"
                        onChange={(event) => {
                          const nextStage = event.target.value as DealFormData['stage'];
                          field.onChange(nextStage);
                          if (!probabilityTouched) {
                            setValue('probability', dealStageDefaultProbability(nextStage));
                          }
                        }}
                      >
                        {DEAL_STAGES.map((stage) => (
                          <MenuItem key={stage.value} value={stage.value}>
                            {stage.label}
                          </MenuItem>
                        ))}
                      </Select>
                      {error && <FormHelperText>{error.message}</FormHelperText>}
                    </FormControl>
                  )}
                />
                <Controller
                  name="probability"
                  control={control}
                  render={({ field, fieldState: { error } }) => (
                    <MuiTextField
                      {...field}
                      value={Number.isNaN(field.value) ? '' : field.value}
                      onChange={(event) => {
                        setProbabilityTouched(true);
                        const text = event.target.value;
                        field.onChange(text === '' ? NaN : Number(text));
                      }}
                      type="number"
                      label="Probability (%)"
                      inputProps={{ min: 0, max: 100, step: 1 }}
                      error={!!error}
                      helperText={error?.message || 'Follows the stage until you change it'}
                      fullWidth
                    />
                  )}
                />
              </Box>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '2fr 1fr 1fr' }} gap={2}>
                <FormTextField
                  name="amount"
                  label="Amount"
                  placeholder="0.00"
                  inputProps={{ inputMode: 'decimal' }}
                  helperText="Decimal amount, stored in minor units"
                />
                <Controller
                  name="currency"
                  control={control}
                  render={({ field, fieldState: { error } }) => (
                    <MuiTextField
                      {...field}
                      onChange={(event) => {
                        setCurrencyTouched(true);
                        field.onChange(event.target.value.toUpperCase());
                      }}
                      label="Currency"
                      inputProps={{ maxLength: 3, style: { textTransform: 'uppercase' } }}
                      error={!!error}
                      helperText={error?.message || 'ISO 4217 code'}
                      fullWidth
                    />
                  )}
                />
                <FormTextField
                  name="expected_close_date"
                  label="Expected close"
                  type="date"
                  InputLabelProps={{ shrink: true }}
                />
              </Box>

              {stageValue === 'lost' && (
                <FormTextField name="lost_reason" label="Lost reason" />
              )}

              <FormTextField name="source" label="Source" />

              <Divider />

              <Typography variant="h6">Links</Typography>

              <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr 1fr' }} gap={2}>
                <CompanyAutocomplete
                  label="Company"
                  value={companyId}
                  initialCompany={company}
                  onChange={(nextId, record) => {
                    setCompanyId(nextId);
                    setCompany(record);
                    clearReferenceError('company_id');
                  }}
                  error={!!referenceErrors.company_id}
                  helperText={linkHelperText('company_id', companyId)}
                />
                <CustomerAutocomplete
                  value={customerId}
                  initialCustomer={customer}
                  onChange={(nextId, record) => {
                    setCustomerId(nextId);
                    setCustomer(record);
                    clearReferenceError('customer_id');
                  }}
                  error={!!referenceErrors.customer_id}
                  helperText={linkHelperText('customer_id', customerId)}
                />
                <LeadAutocomplete
                  value={leadId}
                  initialLead={lead}
                  onChange={(nextId, record) => {
                    setLeadId(nextId);
                    setLead(record);
                    clearReferenceError('lead_id');
                  }}
                  error={!!referenceErrors.lead_id}
                  helperText={linkHelperText('lead_id', leadId)}
                />
              </Box>

              {isAdmin && (
                <Autocomplete
                  value={owner}
                  disableClearable={owner !== null}
                  onChange={(_, newValue) => {
                    setOwner(newValue);
                    setOwnerTouched(true);
                    clearReferenceError('owner_id');
                  }}
                  options={users}
                  getOptionLabel={(option) => `${option.first_name} ${option.last_name}`}
                  isOptionEqualToValue={(option, current) => option.id === current.id}
                  renderInput={(params) => (
                    <MuiTextField
                      {...params}
                      label="Owner"
                      error={!!referenceErrors.owner_id}
                      helperText={
                        referenceErrors.owner_id ??
                        (isEditMode ? 'Leave as is to keep the current owner' : 'Leave empty to own the deal yourself')
                      }
                    />
                  )}
                />
              )}

              <Divider />

              <Typography variant="h6">Additional Information</Typography>

              <FormTextField name="notes" label="Notes" multiline rows={4} />

              <Box display="flex" gap={2} justifyContent="flex-end">
                <Button
                  variant="outlined"
                  startIcon={<CancelIcon />}
                  onClick={() => navigate(isEditMode ? `/deals/${id}` : '/deals')}
                >
                  Cancel
                </Button>
                <Button
                  type="submit"
                  variant="contained"
                  startIcon={<SaveIcon />}
                  disabled={createMutation.isPending || updateMutation.isPending}
                >
                  {isEditMode ? 'Update' : 'Create'} Deal
                </Button>
              </Box>
            </Stack>
          </form>
        </FormProvider>
      </Paper>
    </Box>
  );
};
