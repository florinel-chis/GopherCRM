import React from 'react';
import { Controller, useFormContext } from 'react-hook-form';
import {
  Box,
  FormControlLabel,
  FormHelperText,
  Paper,
  Radio,
  RadioGroup,
  Stack,
  Typography,
} from '@mui/material';
import { FormSelect } from '@/components/form';
import type { User } from '@/types';
import {
  isSubmissionAction,
  toSubmissionAction,
  type FormBuilderValues,
  type SubmissionAction,
} from './formBuilderSchema';

interface SubmissionActionSectionProps {
  readOnly: boolean;
  // The page of GET /users, absent when the caller cannot list users.
  userList?: User[];
  // The signed-in user.
  user: User | null;
}

// The "Submission action" section of the form builder: what a submission
// creates and, for a lead, who owns it.
export const SubmissionActionSection: React.FC<SubmissionActionSectionProps> = ({
  readOnly,
  userList,
  user,
}) => {
  const { control, setValue, watch, trigger, formState } = useFormContext<FormBuilderValues>();

  // One choice drives both flags. A ticket form needs double opt-in, so it is
  // switched on here; no choice ever switches it off.
  const chooseSubmissionAction = (action: SubmissionAction) => {
    const options = { shouldDirty: true };
    setValue('create_lead', action === 'lead', options);
    setValue('create_ticket', action === 'ticket', options);
    if (action === 'ticket') {
      setValue('double_opt_in', true, options);
    }
    // After a failed save, re-check the rules the choice affects so stale
    // errors clear (or new ones show) without another click.
    if (formState.isSubmitted) {
      void trigger(['create_ticket', 'double_opt_in', 'default_owner_id']);
    }
  };

  const createLead = watch('create_lead');
  const createTicket = watch('create_ticket');
  const submissionAction = toSubmissionAction(createLead, createTicket);

  const ownerOptions = (() => {
    const users = (userList || []).filter(
      (candidate) =>
        candidate.is_active && (candidate.role === 'admin' || candidate.role === 'sales')
    );
    const options = users.map((candidate) => ({
      value: candidate.id,
      label: `${candidate.first_name} ${candidate.last_name} (${candidate.email})`,
    }));
    const selected = watch('default_owner_id');
    // Keep the stored owner selectable even when the list is unavailable (sales)
    // or the owner is filtered out of it.
    if (selected && !options.some((option) => option.value === selected)) {
      const label =
        user && user.id === selected
          ? `${user.first_name} ${user.last_name} (you)`
          : `User #${selected}`;
      options.unshift({ value: selected, label });
    }
    return options;
  })();

  return (
    <Paper sx={{ p: 3, mb: 3 }}>
      <Typography variant="h6" gutterBottom>
        Submission action
      </Typography>
      <Stack spacing={3} mt={1}>
        {/* Bound to create_ticket so the exclusivity error lands here; the
            choice itself sets create_lead, create_ticket and, for a
            ticket, double_opt_in. */}
        <Controller
          name="create_ticket"
          control={control}
          render={({ fieldState }) => (
            <Box>
              <RadioGroup
                aria-label="Submission action"
                value={submissionAction}
                onChange={(event) => {
                  if (isSubmissionAction(event.target.value)) {
                    chooseSubmissionAction(event.target.value);
                  }
                }}
              >
                <FormControlLabel
                  value="lead"
                  control={<Radio disabled={readOnly} />}
                  label="Create a lead"
                />
                <FormControlLabel
                  value="ticket"
                  control={<Radio disabled={readOnly} />}
                  label="Create a support ticket"
                />
                <FormControlLabel
                  value="none"
                  control={<Radio disabled={readOnly} />}
                  label="Create nothing"
                />
              </RadioGroup>
              {fieldState.error && (
                <FormHelperText error>{fieldState.error.message}</FormHelperText>
              )}
            </Box>
          )}
        />
        {/* Hidden rather than unmounted, like the other conditional
            inputs: the owner keeps its value while another action is
            chosen and comes back with the lead action. */}
        <Box
          data-testid="lead-action-settings"
          style={{ display: createLead ? 'block' : 'none' }}
        >
          <Stack spacing={3}>
            <FormSelect
              name="default_owner_id"
              label="Lead owner"
              options={ownerOptions}
              disabled={readOnly}
            />
            <Typography variant="body2" color="text.secondary">
              A submission from a known email address is appended to the existing lead
              instead of creating a second one.
            </Typography>
          </Stack>
        </Box>
        {createTicket && (
          <Typography variant="body2" color="text.secondary">
            A confirmed submission opens a ticket for the live customer with that email
            address; otherwise no ticket is opened.
          </Typography>
        )}
      </Stack>
    </Paper>
  );
};
