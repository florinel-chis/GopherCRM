import React, { useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControl,
  FormControlLabel,
  FormGroup,
  FormHelperText,
  FormLabel,
  Switch,
  TextField,
} from '@mui/material';
import {
  webhooksApi,
  WEBHOOK_EVENT_TYPES,
  type WebhookEndpoint,
  type WebhookEndpointCreated,
} from '@/api/endpoints';
import { getAPIError } from '@/utils/apiError';

interface WebhookEndpointDialogProps {
  open: boolean;
  // The endpoint to edit, or null to register a new one.
  endpoint: WebhookEndpoint | null;
  onClose: () => void;
  // The create answer, signing secret included. It is handed over once and
  // kept nowhere in this component.
  onCreated: (created: WebhookEndpointCreated) => void;
  onUpdated: (endpoint: WebhookEndpoint) => void;
}

interface FormState {
  url: string;
  eventTypes: string[];
  isActive: boolean;
}

interface FormErrors {
  message?: string;
  url?: string;
  eventTypes?: string;
}

const initialState = (endpoint: WebhookEndpoint | null): FormState => ({
  url: endpoint?.url ?? '',
  eventTypes: endpoint ? [...endpoint.event_types] : [],
  isActive: endpoint?.is_active ?? true,
});

const detailText = (value: unknown): string | undefined =>
  typeof value === 'string' && value !== '' ? value : undefined;

const EndpointForm: React.FC<Omit<WebhookEndpointDialogProps, 'open'>> = ({
  endpoint,
  onClose,
  onCreated,
  onUpdated,
}) => {
  const [form, setForm] = useState<FormState>(() => initialState(endpoint));
  const [errors, setErrors] = useState<FormErrors>({});
  const [submitting, setSubmitting] = useState(false);

  const toggleEvent = (eventType: string, checked: boolean) =>
    setForm((prev) => ({
      ...prev,
      eventTypes: checked
        ? [...prev.eventTypes, eventType]
        : prev.eventTypes.filter((value) => value !== eventType),
    }));

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const url = form.url.trim();
    // Only presence is checked here. The scheme, credentials, query string
    // and host rules are the server's, and its refusal is what is shown.
    const missing: FormErrors = {};
    if (url === '') {
      missing.url = 'Enter the URL of the endpoint';
    }
    if (form.eventTypes.length === 0) {
      missing.eventTypes = 'Select at least one event';
    }
    if (missing.url || missing.eventTypes) {
      setErrors(missing);
      return;
    }

    // In the order of the shared list, whatever the order of the clicks.
    const input = {
      url,
      event_types: WEBHOOK_EVENT_TYPES.filter((value) => form.eventTypes.includes(value)),
      is_active: form.isActive,
    };
    setErrors({});
    setSubmitting(true);
    try {
      if (endpoint) {
        onUpdated(await webhooksApi.updateEndpoint(endpoint.id, input));
      } else {
        onCreated(await webhooksApi.createEndpoint(input));
      }
    } catch (error) {
      const apiError = getAPIError(error);
      setErrors({
        message: apiError?.message ?? 'The endpoint could not be saved.',
        url: detailText(apiError?.details?.url),
        eventTypes: detailText(apiError?.details?.event_types),
      });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Box component="form" onSubmit={submit} noValidate>
      <DialogTitle id="webhook-endpoint-title">
        {endpoint ? 'Edit webhook endpoint' : 'Add webhook endpoint'}
      </DialogTitle>
      <DialogContent>
        {errors.message && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {errors.message}
          </Alert>
        )}
        <TextField
          label="URL"
          name="url"
          value={form.url}
          onChange={(event) => setForm((prev) => ({ ...prev, url: event.target.value }))}
          fullWidth
          margin="normal"
          autoFocus
          autoComplete="off"
          error={!!errors.url}
          helperText={errors.url ?? 'The HTTPS address the events are sent to.'}
        />
        <FormControl
          component="fieldset"
          margin="normal"
          error={!!errors.eventTypes}
          sx={{ display: 'block' }}
        >
          <FormLabel component="legend">Events</FormLabel>
          <FormGroup>
            {WEBHOOK_EVENT_TYPES.map((eventType) => (
              <FormControlLabel
                key={eventType}
                label={eventType}
                control={
                  <Checkbox
                    size="small"
                    checked={form.eventTypes.includes(eventType)}
                    onChange={(_event, checked) => toggleEvent(eventType, checked)}
                  />
                }
              />
            ))}
          </FormGroup>
          {errors.eventTypes && <FormHelperText>{errors.eventTypes}</FormHelperText>}
        </FormControl>
        <FormControlLabel
          label="Active"
          control={
            <Switch
              checked={form.isActive}
              onChange={(_event, checked) => setForm((prev) => ({ ...prev, isActive: checked }))}
            />
          }
        />
        <FormHelperText>
          Events are queued only for an active endpoint. Deliveries already queued for an endpoint
          that is set inactive fail instead of being sent.
        </FormHelperText>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose} color="inherit">
          Cancel
        </Button>
        <Button type="submit" variant="contained" disabled={submitting}>
          {endpoint ? 'Save' : 'Create'}
        </Button>
      </DialogActions>
    </Box>
  );
};

/**
 * The create and edit form of a webhook endpoint. The form is mounted only
 * while the dialog is open, so every opening starts from the stored values.
 */
export const WebhookEndpointDialog: React.FC<WebhookEndpointDialogProps> = ({
  open,
  onClose,
  ...form
}) => (
  <Dialog
    open={open}
    onClose={onClose}
    fullWidth
    maxWidth="sm"
    aria-labelledby="webhook-endpoint-title"
  >
    {open && <EndpointForm onClose={onClose} {...form} />}
  </Dialog>
);
