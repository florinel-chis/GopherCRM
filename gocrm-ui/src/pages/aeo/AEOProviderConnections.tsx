import { useEffect, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Box, Button, Paper, Stack, TextField, Typography } from '@mui/material';
import { configurationsApi } from '@/api/endpoints/configurations';
import type { AEOProviderStatus } from '@/api/endpoints/aeo';
import { useSnackbar } from '@/hooks/useSnackbar';

const QUERY_KEY = ['configurations', 'category', 'integration'];
const PROVIDERS = [
  { id: 'anthropic', name: 'Anthropic', baseURL: 'https://api.anthropic.com' },
  { id: 'openai', name: 'OpenAI', baseURL: 'https://api.openai.com/v1' },
  {
    id: 'gemini',
    name: 'Gemini',
    baseURL: 'https://generativelanguage.googleapis.com/v1beta/openai/',
  },
  { id: 'kimi', name: 'Kimi', baseURL: 'https://api.moonshot.ai/v1' },
  { id: 'perplexity', name: 'Perplexity', baseURL: 'https://api.perplexity.ai' },
];

interface ConnectionProps {
  provider: (typeof PROVIDERS)[number];
  savedModel: string;
  savedBaseURL: string;
  effectiveModel?: string;
}

function ProviderConnection({
  provider,
  savedModel,
  savedBaseURL,
  effectiveModel,
}: ConnectionProps) {
  const [model, setModel] = useState(savedModel);
  const [baseURL, setBaseURL] = useState(savedBaseURL);
  const [validationError, setValidationError] = useState('');
  const queryClient = useQueryClient();
  const { showSuccess, showError } = useSnackbar();

  useEffect(() => {
    setModel(savedModel);
    setBaseURL(savedBaseURL);
  }, [savedModel, savedBaseURL]);

  const mutation = useMutation({
    mutationFn: async () => {
      const prefix = `integration.aeo.${provider.id}_`;
      // Save only edited values. A failed second write still refreshes the
      // form below so a successful first write is represented accurately.
      if (model.trim() !== savedModel) {
        await configurationsApi.set(`${prefix}model`, { value: model.trim() });
      }
      if (baseURL.trim() !== savedBaseURL) {
        await configurationsApi.set(`${prefix}base_url`, { value: baseURL.trim() });
      }
    },
    onSuccess: () => showSuccess(`${provider.name} connection saved`),
    onError: () => showError(`Failed to save ${provider.name} connection`),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: QUERY_KEY });
      queryClient.invalidateQueries({ queryKey: ['aeo', 'providers'] });
    },
  });

  const save = (event: React.FormEvent) => {
    event.preventDefault();
    if (Array.from(model.trim()).length > 120) {
      setValidationError('Model must be 120 characters or fewer');
      return;
    }
    if (baseURL.trim()) {
      try {
        const url = new URL(baseURL.trim());
        if (
          !['http:', 'https:'].includes(url.protocol) ||
          url.username ||
          url.password ||
          url.search ||
          url.hash ||
          baseURL.includes('?') ||
          baseURL.includes('#')
        ) {
          throw new Error('Invalid base URL');
        }
      } catch {
        setValidationError(
          'Use an HTTP(S) base URL without credentials, query parameters or a fragment',
        );
        return;
      }
    }
    setValidationError('');
    mutation.mutate();
  };

  return (
    <Box component="form" onSubmit={save} noValidate>
      <Typography variant="subtitle1" mb={1}>
        {provider.name}
      </Typography>
      <Stack direction={{ xs: 'column', md: 'row' }} spacing={2} alignItems="flex-start">
        <TextField
          size="small"
          fullWidth
          label={`${provider.name} model`}
          placeholder={effectiveModel || 'Server default'}
          helperText={
            effectiveModel ? `Current model: ${effectiveModel}` : 'Blank uses the server default'
          }
          value={model}
          disabled={mutation.isPending}
          onChange={(event) => setModel(event.target.value)}
        />
        <TextField
          size="small"
          fullWidth
          label={`${provider.name} base URL`}
          placeholder={provider.baseURL}
          helperText={
            provider.id === 'anthropic'
              ? 'Messages API root, without /v1/messages'
              : 'OpenAI-compatible base URL, without /chat/completions'
          }
          value={baseURL}
          disabled={mutation.isPending}
          onChange={(event) => setBaseURL(event.target.value)}
        />
        <Button
          type="submit"
          variant="outlined"
          aria-label={`Save ${provider.name} connection`}
          disabled={
            mutation.isPending || (model.trim() === savedModel && baseURL.trim() === savedBaseURL)
          }
        >
          Save
        </Button>
      </Stack>
      {validationError && (
        <Alert severity="error" sx={{ mt: 1 }}>
          {validationError}
        </Alert>
      )}
    </Box>
  );
}

export function AEOProviderConnections({ providers }: { providers: AEOProviderStatus[] }) {
  const { data, isLoading, isError } = useQuery({
    queryKey: QUERY_KEY,
    queryFn: () => configurationsApi.getByCategory('integration'),
  });
  return (
    <Paper sx={{ p: 3, mb: 3 }}>
      <Typography variant="h6" gutterBottom>
        Models and endpoints
      </Typography>
      <Typography variant="body2" color="text.secondary" mb={2}>
        Changes apply to the next run and prompt suggestions without a restart. Leave a field blank
        to use the server setting or provider default. Use endpoints you trust with your API key.
      </Typography>
      {isError ? (
        <Alert severity="error">Failed to load provider connections</Alert>
      ) : isLoading ? (
        <Typography>Loading connections…</Typography>
      ) : (
        <Stack spacing={3}>
          {PROVIDERS.map((provider) => (
            <ProviderConnection
              key={provider.id}
              provider={provider}
              savedModel={
                data?.find((item) => item.key === `integration.aeo.${provider.id}_model`)?.value ||
                ''
              }
              savedBaseURL={
                data?.find((item) => item.key === `integration.aeo.${provider.id}_base_url`)
                  ?.value || ''
              }
              effectiveModel={providers.find((item) => item.name === provider.id)?.model}
            />
          ))}
        </Stack>
      )}
    </Paper>
  );
}
