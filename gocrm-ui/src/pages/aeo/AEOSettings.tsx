import React, { useEffect, useState } from 'react';
import { useForm, useFieldArray, FormProvider, useFormContext } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  Divider,
  IconButton,
  MenuItem,
  Paper,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  PlayArrow as PlayArrowIcon,
} from '@mui/icons-material';
import { AxiosError } from 'axios';
import { FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { aeoApi, type AEOProfile, type SaveAEOProfileData } from '@/api/endpoints/aeo';
import { configurationsApi } from '@/api/endpoints/configurations';

// The provider keys live in the configurations system as sensitive values: the
// API never echoes them back, it only reports whether one is stored.
const PROVIDER_KEYS = [
  { key: 'integration.aeo.anthropic_api_key', name: 'Anthropic' },
  { key: 'integration.aeo.openai_api_key', name: 'OpenAI' },
  { key: 'integration.aeo.gemini_api_key', name: 'Gemini' },
  { key: 'integration.aeo.moonshot_api_key', name: 'Kimi' },
  { key: 'integration.aeo.perplexity_api_key', name: 'Perplexity' },
] as const;

const INTEGRATION_CONFIG_KEY = ['configurations', 'category', 'integration'];

const GENERATION_ENGINE_KEY = 'integration.aeo.generation_engine';

const GENERATION_ENGINES = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'perplexity', label: 'Perplexity' },
];

// Mirrors the API binding tags on PUT /aeo/profile: brand_name required and
// ≤120 chars, description ≤2000, at most 20 aliases, domains and competitors.
const competitorSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Competitor name is required')
    .max(120, 'Name must be 120 characters or fewer'),
  domain: z.string().trim().max(255, 'Domain must be 255 characters or fewer'),
  aliases: z.array(z.string()).max(20, 'At most 20 aliases per competitor'),
});

const profileSchema = z.object({
  brand_name: z
    .string()
    .trim()
    .min(1, 'Brand name is required')
    .max(120, 'Brand name must be 120 characters or fewer'),
  description: z.string().max(2000, 'Description must be 2000 characters or fewer'),
  brand_aliases: z.array(z.string()).max(20, 'At most 20 brand aliases'),
  owned_domains: z.array(z.string()).max(20, 'At most 20 owned domains'),
  competitors: z.array(competitorSchema).max(20, 'At most 20 competitors'),
});

type ProfileFormData = z.infer<typeof profileSchema>;

const emptyProfile: ProfileFormData = {
  brand_name: '',
  description: '',
  brand_aliases: [],
  owned_domains: [],
  competitors: [],
};

const toFormValues = (profile: AEOProfile | null | undefined): ProfileFormData => {
  if (!profile) {
    return emptyProfile;
  }
  return {
    brand_name: profile.brand_name ?? '',
    description: profile.description ?? '',
    brand_aliases: profile.brand_aliases ?? [],
    owned_domains: profile.owned_domains ?? [],
    competitors: (profile.competitors ?? []).map((competitor) => ({
      name: competitor.name ?? '',
      domain: competitor.domain ?? '',
      aliases: competitor.aliases ?? [],
    })),
  };
};

interface StringListEditorProps {
  label: string;
  placeholder: string;
  values: string[];
  disabled?: boolean;
  onChange: (values: string[]) => void;
}

// Small chip editor used for brand aliases, owned domains and per-competitor
// aliases. Kept local: nothing else in the app needs a free-text chip list yet.
const StringListEditor: React.FC<StringListEditorProps> = ({
  label,
  placeholder,
  values,
  disabled,
  onChange,
}) => {
  const [draft, setDraft] = useState('');

  const commit = () => {
    const value = draft.trim();
    if (!value || values.includes(value)) {
      setDraft('');
      return;
    }
    onChange([...values, value]);
    setDraft('');
  };

  return (
    <Box>
      <Stack direction="row" spacing={1} mb={1}>
        <TextField
          size="small"
          fullWidth
          label={label}
          placeholder={placeholder}
          value={draft}
          disabled={disabled}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              commit();
            }
          }}
        />
        <Button
          variant="outlined"
          onClick={commit}
          disabled={disabled}
          aria-label={`Add ${label}`}
        >
          Add
        </Button>
      </Stack>
      <Box display="flex" gap={1} flexWrap="wrap">
        {values.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            None yet.
          </Typography>
        ) : (
          values.map((value) => (
            <Chip
              key={value}
              label={value}
              onDelete={disabled ? undefined : () => onChange(values.filter((v) => v !== value))}
            />
          ))
        )}
      </Box>
    </Box>
  );
};

interface CompetitorAliasesProps {
  index: number;
  disabled: boolean;
}

const CompetitorAliases: React.FC<CompetitorAliasesProps> = ({ index, disabled }) => {
  const { watch, setValue } = useFormContext<ProfileFormData>();
  const aliases = watch(`competitors.${index}.aliases`) || [];

  return (
    <StringListEditor
      label={`Competitor ${index + 1} aliases`}
      placeholder="Globex Corp"
      values={aliases}
      disabled={disabled}
      onChange={(next) =>
        setValue(`competitors.${index}.aliases`, next, { shouldDirty: true })
      }
    />
  );
};

// Admin-only editor for the answer-engine API keys. Inputs start empty and are
// cleared again after every write — a stored key is never rendered.
const ProviderKeysCard: React.FC = () => {
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();
  const [drafts, setDrafts] = useState<Record<string, string>>({});

  const {
    data: configurations,
    isLoading,
    isError,
  } = useQuery({
    queryKey: INTEGRATION_CONFIG_KEY,
    queryFn: () => configurationsApi.getByCategory('integration'),
  });

  const keyMutation = useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      configurationsApi.set(key, { value }),
    onSuccess: (_result, variables) => {
      setDrafts((current) => ({ ...current, [variables.key]: '' }));
      showSuccess(variables.value === '' ? 'API key cleared' : 'API key saved');
      queryClient.invalidateQueries({ queryKey: INTEGRATION_CONFIG_KEY });
      // The provider roster reflects the new key without a restart.
      queryClient.invalidateQueries({ queryKey: ['aeo', 'providers'] });
    },
    onError: (_error, variables) => {
      showError(
        variables.value === '' ? 'Failed to clear the API key' : 'Failed to save the API key'
      );
    },
  });

  const engineMutation = useMutation({
    mutationFn: (value: string) => configurationsApi.set(GENERATION_ENGINE_KEY, { value }),
    onSuccess: () => {
      showSuccess('Prompt generation engine updated');
      queryClient.invalidateQueries({ queryKey: INTEGRATION_CONFIG_KEY });
    },
    onError: () => {
      showError('Failed to update the prompt generation engine');
    },
  });

  const pendingKey = keyMutation.isPending ? keyMutation.variables?.key : undefined;

  return (
    <Paper sx={{ p: 3, mb: 3 }}>
      <Typography variant="h6" gutterBottom>
        API keys
      </Typography>
      <Typography variant="body2" color="text.secondary" mb={2}>
        Keys are stored encrypted and never shown again. Saving one takes effect immediately; an
        empty key falls back to the server environment.
      </Typography>

      {isError ? (
        <Alert severity="error">Failed to load the stored API keys</Alert>
      ) : isLoading ? (
        <Typography variant="body2" color="text.secondary">
          Loading stored keys…
        </Typography>
      ) : (
        <Stack spacing={2}>
          {PROVIDER_KEYS.map((provider) => {
            const config = (configurations || []).find((c) => c.key === provider.key);
            const isSet = Boolean(config?.is_set);
            const draft = drafts[provider.key] ?? '';
            const busy = pendingKey === provider.key;

            return (
              <Stack
                key={provider.key}
                direction={{ xs: 'column', sm: 'row' }}
                spacing={1}
                alignItems={{ xs: 'stretch', sm: 'center' }}
              >
                <Typography variant="body2" sx={{ minWidth: 96 }}>
                  {provider.name}
                </Typography>
                <Chip
                  size="small"
                  label={isSet ? 'Configured' : 'Not configured'}
                  color={isSet ? 'success' : 'default'}
                  variant={isSet ? 'filled' : 'outlined'}
                  data-testid={`api-key-chip-${provider.name.toLowerCase()}`}
                />
                <TextField
                  size="small"
                  fullWidth
                  type="password"
                  autoComplete="new-password"
                  label={`${provider.name} API key`}
                  placeholder="enter new key"
                  value={draft}
                  disabled={busy}
                  onChange={(event) =>
                    setDrafts((current) => ({ ...current, [provider.key]: event.target.value }))
                  }
                />
                <Button
                  variant="outlined"
                  aria-label={`Save ${provider.name} API key`}
                  disabled={busy || draft.trim() === ''}
                  onClick={() =>
                    keyMutation.mutate({ key: provider.key, value: draft.trim() })
                  }
                >
                  Save
                </Button>
                <Button
                  color="error"
                  aria-label={`Clear ${provider.name} API key`}
                  disabled={busy || !isSet}
                  onClick={() => keyMutation.mutate({ key: provider.key, value: '' })}
                >
                  Clear
                </Button>
              </Stack>
            );
          })}

          <Divider />
          <Stack
            direction={{ xs: 'column', sm: 'row' }}
            spacing={1}
            alignItems={{ xs: 'stretch', sm: 'center' }}
          >
            <Typography variant="body2" sx={{ minWidth: 96 }}>
              Generation
            </Typography>
            <TextField
              select
              size="small"
              fullWidth
              label="Prompt generation engine"
              helperText="Prompt suggestions are generated by this engine; it needs its API key configured."
              value={
                (configurations || []).find((c) => c.key === GENERATION_ENGINE_KEY)?.value ||
                'anthropic'
              }
              disabled={engineMutation.isPending}
              onChange={(event) => engineMutation.mutate(event.target.value)}
              slotProps={{ htmlInput: { 'data-testid': 'generation-engine-select' } }}
            >
              {GENERATION_ENGINES.map((engine) => (
                <MenuItem key={engine.value} value={engine.value}>
                  {engine.label}
                </MenuItem>
              ))}
            </TextField>
          </Stack>
        </Stack>
      )}
    </Paper>
  );
};

export const Component: React.FC = () => {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // The API allows admin and sales to write the profile and to start a run;
  // support is read-only here, and customers never reach the route at all.
  const canManage = user?.role === 'admin' || user?.role === 'sales';
  // Only admins may read or write configurations, so only they get the key card.
  const isAdmin = user?.role === 'admin';

  const {
    data: profile,
    isLoading: profileLoading,
    isError: profileError,
    error: profileErrorObject,
  } = useQuery({
    queryKey: ['aeo', 'profile'],
    queryFn: () => aeoApi.getProfile(),
  });

  const {
    data: providers,
    isLoading: providersLoading,
    isError: providersError,
  } = useQuery({
    queryKey: ['aeo', 'providers'],
    queryFn: () => aeoApi.getProviders(),
  });

  const methods = useForm<ProfileFormData>({
    resolver: zodResolver(profileSchema),
    defaultValues: emptyProfile,
  });
  const { control, reset, watch, setValue, handleSubmit } = methods;
  const competitors = useFieldArray({ control, name: 'competitors' });

  // The form is populated once the profile query settles. `reset` also clears
  // the dirty state so a refetch does not look like unsaved work.
  useEffect(() => {
    if (!profileLoading) {
      reset(toFormValues(profile));
    }
  }, [profile, profileLoading, reset]);

  const saveMutation = useMutation({
    mutationFn: (data: SaveAEOProfileData) => aeoApi.saveProfile(data),
    onSuccess: (saved) => {
      showSuccess('Brand profile saved');
      queryClient.setQueryData(['aeo', 'profile'], saved);
      queryClient.invalidateQueries({ queryKey: ['aeo'] });
    },
    onError: () => {
      showError('Failed to save the brand profile');
    },
  });

  const runMutation = useMutation({
    mutationFn: () => aeoApi.createRun(),
    onSuccess: () => {
      showSuccess('AEO run started');
      queryClient.invalidateQueries({ queryKey: ['aeo'] });
    },
    onError: (error: unknown) => {
      // 409 covers both "a run is already in progress" and "no brand profile
      // yet"; the server message distinguishes them. 503 means no provider key
      // is configured, which is an operator problem, not a user error.
      if (error instanceof AxiosError) {
        const message = (error.response?.data as { message?: string } | undefined)?.message;
        if (error.response?.status === 409) {
          showError(message || 'An AEO run is already in progress');
          return;
        }
        if (error.response?.status === 503) {
          showError(message || 'No AEO providers are configured');
          return;
        }
      }
      showError('Failed to start an AEO run');
    },
  });

  const onSubmit = (data: ProfileFormData) => {
    saveMutation.mutate({
      brand_name: data.brand_name.trim(),
      description: data.description,
      brand_aliases: data.brand_aliases,
      owned_domains: data.owned_domains,
      competitors: data.competitors.map((competitor) => ({
        name: competitor.name.trim(),
        domain: competitor.domain.trim(),
        aliases: competitor.aliases,
      })),
    });
  };

  if (profileLoading || providersLoading) {
    return <Loading />;
  }

  if (profileError) {
    return (
      <Alert severity="error">
        Failed to load the AEO brand profile
        {profileErrorObject instanceof Error ? `: ${profileErrorObject.message}` : ''}
      </Alert>
    );
  }

  const brandAliases = watch('brand_aliases') || [];
  const ownedDomains = watch('owned_domains') || [];
  const providerList = providers || [];
  const configuredCount = providerList.filter((provider) => provider.configured).length;

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">AEO Settings</Typography>
        {canManage && (
          <Button
            variant="contained"
            startIcon={<PlayArrowIcon />}
            onClick={() => runMutation.mutate()}
            disabled={runMutation.isPending}
          >
            Run now
          </Button>
        )}
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Answer engines
        </Typography>
        {providersError ? (
          <Alert severity="error">Failed to load provider status</Alert>
        ) : providerList.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            No AEO data yet — no answer engines are compiled into this build.
          </Typography>
        ) : (
          <>
            <Box display="flex" gap={1} flexWrap="wrap" mb={1}>
              {providerList.map((provider) => (
                <Tooltip
                  key={provider.name}
                  title={
                    provider.configured
                      ? `${provider.model}`
                      : `${provider.model} — no API key configured`
                  }
                >
                  <Chip
                    label={`${provider.name}: ${provider.model}`}
                    color={provider.configured ? 'success' : 'default'}
                    variant={provider.configured ? 'filled' : 'outlined'}
                    data-testid={`provider-chip-${provider.name}`}
                  />
                </Tooltip>
              ))}
            </Box>
            <Typography variant="body2" color="text.secondary">
              {configuredCount} of {providerList.length} engines configured. Keys come from the
              admin key settings or the server environment; an engine without a key is skipped by
              every run.
            </Typography>
          </>
        )}
      </Paper>

      {isAdmin && <ProviderKeysCard />}

      {!profile && (
        <Alert severity="info" sx={{ mb: 3 }}>
          No brand profile configured yet. Save one below before starting a run.
        </Alert>
      )}

      <FormProvider {...methods}>
        <form onSubmit={handleSubmit(onSubmit)} noValidate>
          <Paper sx={{ p: 3, mb: 3 }}>
            <Typography variant="h6" gutterBottom>
              Brand profile
            </Typography>
            <Stack spacing={3} mt={1}>
              <FormTextField
                name="brand_name"
                label="Brand name"
                required
                disabled={!canManage}
              />
              <FormTextField
                name="description"
                label="Business description"
                multiline
                minRows={3}
                disabled={!canManage}
                helperText="Used as context when generating prompt suggestions."
              />
              <StringListEditor
                label="Brand aliases"
                placeholder="Acme Inc"
                values={brandAliases}
                disabled={!canManage}
                onChange={(next) => setValue('brand_aliases', next, { shouldDirty: true })}
              />
              <StringListEditor
                label="Owned domains"
                placeholder="acme.com"
                values={ownedDomains}
                disabled={!canManage}
                onChange={(next) => setValue('owned_domains', next, { shouldDirty: true })}
              />
            </Stack>
          </Paper>

          <Paper sx={{ p: 3, mb: 3 }}>
            <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
              <Typography variant="h6">Competitors</Typography>
              {canManage && (
                <Button
                  startIcon={<AddIcon />}
                  onClick={() => competitors.append({ name: '', domain: '', aliases: [] })}
                >
                  Add competitor
                </Button>
              )}
            </Box>

            {competitors.fields.length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No competitors tracked yet.
              </Typography>
            ) : (
              <Stack spacing={2}>
                {competitors.fields.map((field, index) => (
                  <Card key={field.id} variant="outlined">
                    <CardContent>
                      <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
                        <Typography variant="subtitle1">Competitor {index + 1}</Typography>
                        {canManage && (
                          <Tooltip title="Remove competitor">
                            <IconButton
                              size="small"
                              aria-label={`Remove competitor ${index + 1}`}
                              onClick={() => competitors.remove(index)}
                            >
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                      <Stack spacing={2}>
                        <FormTextField
                          name={`competitors.${index}.name`}
                          label={`Competitor ${index + 1} name`}
                          required
                          disabled={!canManage}
                        />
                        <FormTextField
                          name={`competitors.${index}.domain`}
                          label={`Competitor ${index + 1} domain`}
                          disabled={!canManage}
                        />
                        <Divider />
                        <CompetitorAliases index={index} disabled={!canManage} />
                      </Stack>
                    </CardContent>
                  </Card>
                ))}
              </Stack>
            )}
          </Paper>

          {canManage && (
            <Box display="flex" justifyContent="flex-end">
              <Button type="submit" variant="contained" disabled={saveMutation.isPending}>
                Save profile
              </Button>
            </Box>
          )}
        </form>
      </FormProvider>
    </Box>
  );
};

Component.displayName = 'AEOSettings';
