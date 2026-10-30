import React from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Typography,
  Box,
  Alert,
} from '@mui/material';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { FormTextField } from '@/components/form';
import type { Lead } from '@/types';
import { customerWebsiteSchema } from '@/utils/customerWebsite';
import { getAPIError } from '@/utils/apiError';

// The server's wording for the same refusal (leads.conversion.require_notes).
const NOTES_REQUIRED_MESSAGE = 'Conversion notes are required.';

const baseSchema = z.object({
  company_name: z.string().optional(),
  website: customerWebsiteSchema,
  address: z.string().optional(),
  notes: z.string().optional(),
});

// Mirrors the server: with notes required, whitespace alone is no note.
const notesRequiredSchema = baseSchema.extend({
  notes: z.string().optional().refine((value) => !!value?.trim(), NOTES_REQUIRED_MESSAGE),
});

type ConvertLeadFormData = z.infer<typeof baseSchema>;

interface ConversionRefusal {
  /** The form field the server named, when the form has it. */
  field?: 'notes';
  message: string;
}

// Reads a failed POST /leads/:id/convert: a VALIDATION_ERROR naming the notes
// belongs on the notes field; anything else (a status the settings do not
// allow, an already converted lead, a lead without an email) is shown above
// the form with the server's own message.
const readRefusal = (error: unknown): ConversionRefusal => {
  const apiError = getAPIError(error);
  const message = apiError?.message || 'Failed to convert lead';
  if (apiError?.code === 'VALIDATION_ERROR' && apiError.details?.field === 'notes') {
    return { field: 'notes', message };
  }
  return { message };
};

export interface ConvertLeadData {
  company_name?: string;
  website?: string;
  address?: string;
  notes?: string;
}

interface ConvertLeadDialogProps {
  open: boolean;
  lead: Lead | null;
  onClose: () => void;
  onConfirm: (data: ConvertLeadData) => void;
  isLoading?: boolean;
  /** leads.conversion.require_notes: notes must not be blank. */
  requireNotes?: boolean;
  /** leads.conversion.auto_assign_owner: the lead owner becomes the customer's assigned user. */
  autoAssignOwner?: boolean;
  /** The last failed conversion attempt, shown in the dialog; null once retried. */
  error?: unknown;
  /**
   * Whether the conversion settings are known. While they are not, the
   * dialog claims no rule and does not convert; `onRetrySettings` reloads
   * them.
   */
  settingsStatus?: 'ready' | 'loading' | 'unavailable';
  onRetrySettings?: () => void;
}

/**
 * The form lives in a body keyed by the lead id: another lead starts from a
 * fresh form, while a re-read of the same lead (after a refusal, say) keeps
 * what the user typed.
 */
export const ConvertLeadDialog: React.FC<ConvertLeadDialogProps> = ({ lead, ...props }) =>
  lead ? <ConvertLeadDialogBody key={lead.id} lead={lead} {...props} /> : null;

const ConvertLeadDialogBody: React.FC<ConvertLeadDialogProps & { lead: Lead }> = ({
  open,
  lead,
  onClose,
  onConfirm,
  isLoading = false,
  requireNotes = false,
  autoAssignOwner = false,
  error = null,
  settingsStatus = 'ready',
  onRetrySettings,
}) => {
  const settingsKnown = settingsStatus === 'ready';
  const methods = useForm<ConvertLeadFormData>({
    resolver: zodResolver(requireNotes ? notesRequiredSchema : baseSchema),
    defaultValues: {
      company_name: lead.company_name || '',
      website: '',
      address: '',
      notes: '',
    },
  });

  const refusal = error ? readRefusal(error) : null;

  // The server still refused the notes (for instance the setting was turned
  // on after this page loaded): show its message on the field, again on
  // every refused attempt.
  const { setError } = methods;
  React.useEffect(() => {
    const refused = error ? readRefusal(error) : null;
    if (refused?.field === 'notes') {
      setError('notes', { type: 'server', message: refused.message });
    }
  }, [error, setError]);

  const handleSubmit = (data: ConvertLeadFormData) => {
    if (!settingsKnown) return;
    // Filter out empty strings
    const filteredData: ConvertLeadData = {};
    if (data.company_name?.trim()) filteredData.company_name = data.company_name.trim();
    if (data.website?.trim()) filteredData.website = data.website.trim();
    if (data.address?.trim()) filteredData.address = data.address.trim();
    if (data.notes?.trim()) filteredData.notes = data.notes.trim();

    onConfirm(filteredData);
  };

  const handleClose = () => {
    methods.reset();
    onClose();
  };

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="md" fullWidth>
      <DialogTitle>Convert Lead to Customer</DialogTitle>

      <DialogContent>
        <Box sx={{ mb: 3 }}>
          <Alert severity="info" sx={{ mb: 2 }}>
            Converting this lead will create a new customer record. The lead will be marked as
            "converted" and linked to the new customer.
            {settingsKnown && autoAssignOwner && (
              <>
                {' '}
                The lead's owner becomes the customer's assigned user.
              </>
            )}
          </Alert>

          {!settingsKnown && (
            <Alert
              severity="warning"
              sx={{ mb: 2 }}
              data-testid="convert-lead-settings"
              action={
                settingsStatus === 'unavailable' && onRetrySettings ? (
                  <Button color="inherit" size="small" onClick={onRetrySettings}>
                    Retry
                  </Button>
                ) : undefined
              }
            >
              {settingsStatus === 'loading'
                ? 'Loading the conversion settings…'
                : 'Could not load the conversion settings, so the lead cannot be converted yet.'}
            </Alert>
          )}

          {refusal && !refusal.field && (
            <Alert severity="error" sx={{ mb: 2 }} data-testid="convert-lead-error">
              {refusal.message}
            </Alert>
          )}

          <Typography variant="h6" gutterBottom>
            Lead Information
          </Typography>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
            <strong>Contact:</strong> {lead.contact_name}
            <br />
            <strong>Email:</strong> {lead.email}
            <br />
            <strong>Phone:</strong> {lead.phone}
            <br />
            <strong>Current Company:</strong> {lead.company_name || 'Not specified'}
          </Typography>
        </Box>

        <FormProvider {...methods}>
          <Box component="form" onSubmit={methods.handleSubmit(handleSubmit)}>
            <Typography variant="h6" gutterBottom>
              {settingsKnown && !requireNotes
                ? 'Additional Customer Information (Optional)'
                : 'Additional Customer Information'}
            </Typography>
            <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
              You can update or add additional information for the new customer record.
              {settingsKnown && requireNotes && ' Conversion notes are required.'}
            </Typography>

            <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
              <FormTextField
                name="company_name"
                label="Company Name"
                placeholder="Leave empty to use current company name"
                fullWidth
              />

              <FormTextField
                name="website"
                label="Website"
                placeholder="https://example.com"
                fullWidth
              />

              <FormTextField
                name="address"
                label="Address"
                placeholder="Company address"
                multiline
                rows={2}
                fullWidth
              />

              <FormTextField
                name="notes"
                label="Conversion Notes"
                placeholder="Add any notes about this conversion..."
                multiline
                rows={3}
                fullWidth
                required={settingsKnown && requireNotes}
              />
            </Box>
          </Box>
        </FormProvider>
      </DialogContent>

      <DialogActions>
        <Button onClick={handleClose} disabled={isLoading}>
          Cancel
        </Button>
        <Button
          onClick={methods.handleSubmit(handleSubmit)}
          variant="contained"
          color="primary"
          disabled={isLoading || !settingsKnown}
        >
          {isLoading ? 'Converting...' : 'Convert to Customer'}
        </Button>
      </DialogActions>
    </Dialog>
  );
};
