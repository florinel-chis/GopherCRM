import React from 'react';
import { Controller, useFormContext } from 'react-hook-form';
import {
  Box,
  Divider,
  FormControlLabel,
  Paper,
  Radio,
  RadioGroup,
  Stack,
  Typography,
} from '@mui/material';
import { FormSwitch, FormTextField } from '@/components/form';
import { arrayError, type FormBuilderValues } from './formBuilderSchema';
import { StringListEditor } from './StringListEditor';

interface FormSettingsSectionsProps {
  readOnly: boolean;
}

// The "After submission", "Notifications", "Follow-up and double opt-in" and
// "Spam protection" sections of the form builder.
export const FormSettingsSections: React.FC<FormSettingsSectionsProps> = ({ readOnly }) => {
  const { control, setValue, watch, formState } = useFormContext<FormBuilderValues>();

  const submitAction = watch('submit_action');
  const doubleOptIn = watch('double_opt_in');
  const notifyEmails = watch('notify_emails') || [];
  const allowedDomains = watch('allowed_domains') || [];
  // A cross-field issue on an array lands on the array itself — as `root` once
  // the array has entries — rather than on one of them, where react-hook-form's
  // typing expects a list.
  const notifyEmailsError = arrayError(formState.errors.notify_emails);
  const allowedDomainsError = arrayError(formState.errors.allowed_domains);

  return (
    <>
      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          After submission
        </Typography>
        <Controller
          name="submit_action"
          control={control}
          render={({ field }) => (
            <RadioGroup {...field} row>
              <FormControlLabel
                value="message"
                control={<Radio disabled={readOnly} />}
                label="Show a thank-you message"
              />
              <FormControlLabel
                value="redirect"
                control={<Radio disabled={readOnly} />}
                label="Redirect to a URL"
              />
            </RadioGroup>
          )}
        />
        <Stack spacing={3} mt={2}>
          {/* Both stay mounted: react-hook-form drops the value of an
              unmounted input at submit time, and a missing required key
              fails validation with nothing on screen to fix. */}
          <Box sx={{ display: submitAction === 'message' ? 'block' : 'none' }}>
            <FormTextField
              name="thank_you_message"
              label="Thank-you message"
              multiline
              minRows={2}
              disabled={readOnly}
              helperText="Left empty, the server uses its default wording."
            />
          </Box>
          <Box sx={{ display: submitAction === 'redirect' ? 'block' : 'none' }}>
            <FormTextField
              name="redirect_url"
              label="Redirect URL"
              disabled={readOnly}
              helperText="http(s) only."
            />
          </Box>
          <Divider />
          <FormTextField
            name="consent_text"
            label="Consent text"
            multiline
            minRows={2}
            disabled={readOnly}
            helperText="When set, the rendered form shows a required consent checkbox with this text."
          />
        </Stack>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Notifications
        </Typography>
        <Box mt={2}>
          <StringListEditor
            label="Notification recipients"
            placeholder="sales@example.com"
            values={notifyEmails}
            disabled={readOnly}
            error={notifyEmailsError}
            helperText="Notified on every non-spam submission."
            onChange={(next) => setValue('notify_emails', next, { shouldDirty: true })}
          />
        </Box>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Follow-up and double opt-in
        </Typography>
        <Stack spacing={3} mt={1}>
          <FormSwitch
            name="double_opt_in"
            label="Require email confirmation before a lead or ticket is created"
            disabled={readOnly}
          />
          <Box sx={{ display: doubleOptIn ? 'block' : 'none' }}>
            <Stack spacing={3}>
              <FormTextField
                name="confirmation_subject"
                label="Confirmation subject"
                disabled={readOnly}
              />
              <FormTextField
                name="confirmation_body"
                label="Confirmation body"
                multiline
                minRows={3}
                disabled={readOnly}
                helperText="Use {confirmation_link} where the confirmation link belongs. Defaults are used when left empty."
              />
            </Stack>
          </Box>
          <Divider />
          <FormTextField
            name="follow_up_subject"
            label="Follow-up subject"
            disabled={readOnly}
            helperText={
              doubleOptIn
                ? 'Sent after the address is confirmed. Leave empty to send nothing.'
                : 'Sent right after a submission. Leave empty to send nothing.'
            }
          />
          <FormTextField
            name="follow_up_body"
            label="Follow-up body"
            multiline
            minRows={3}
            disabled={readOnly}
            helperText="Use {content_link} where the gated-content link belongs."
          />
          <FormTextField
            name="content_url"
            label="Content URL"
            disabled={readOnly}
            helperText="Substituted for {content_link} in the follow-up email."
          />
        </Stack>
      </Paper>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Typography variant="h6" gutterBottom>
          Spam protection
        </Typography>
        <Stack spacing={3} mt={1}>
          <Typography variant="body2" color="text.secondary">
            A honeypot field and a time trap are always active. reCAPTCHA is an extra layer.
          </Typography>
          <FormSwitch
            name="captcha_enabled"
            label="Verify submissions with reCAPTCHA"
            disabled={readOnly}
          />
          <Typography variant="body2" color="text.secondary">
            Requires reCAPTCHA keys in the server environment; without them the layer is
            skipped.
          </Typography>
          <StringListEditor
            label="Allowed domains"
            placeholder="example.com"
            values={allowedDomains}
            disabled={readOnly}
            error={allowedDomainsError}
            helperText="Bare hosts only. Empty means any site may embed this form; subdomains must be listed separately."
            onChange={(next) => setValue('allowed_domains', next, { shouldDirty: true })}
          />
        </Stack>
      </Paper>
    </>
  );
};
