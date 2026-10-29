import React, { useEffect, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Controller, FormProvider, useFieldArray, useForm, useWatch } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  FormControlLabel,
  FormHelperText,
  Grid,
  IconButton,
  Menu,
  MenuItem,
  Paper,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  ArrowDownward as DownIcon,
  ArrowUpward as UpIcon,
  Delete as DeleteIcon,
  Science as DryRunIcon,
} from '@mui/icons-material';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { Loading } from '@/components/Loading';
import { useSnackbar } from '@/hooks/useSnackbar';
import { automationApi, type AutomationRuleInput } from '@/api/endpoints';
import {
  AUTOMATION_MAX_STEPS,
  AUTOMATION_STEP_TYPES,
  AUTOMATION_TRIGGERS,
  dealStageLabel,
  isChangeTrigger,
  type AutomationEntityType,
  type AutomationTriggerType,
  type DealStage,
} from '@/types';
import { getAPIError } from '@/utils/apiError';
import { stepTypeLabel, triggerTypeLabel } from './automationFormat';
import { afterRuleChanged, afterRuleCreated, automationKeys } from './automationQueries';
import { DryRunDialog } from './DryRunDialog';
import { RuleStepFields } from './RuleStepFields';
import {
  emptyFormValues,
  emptyStep,
  entityValues,
  fromRule,
  parseStepError,
  ruleFormSchema,
  toRulePayload,
  type RuleFormValues,
} from './ruleFormSchema';

type ScalarKey = 'name' | 'description' | 'from' | 'to' | 'filter_value';

interface ServerError {
  step?: number;
  text: string;
}

const valueLabel = (entity: AutomationEntityType, value: string): string =>
  entity === 'deal' ? dealStageLabel(value as DealStage) : value.charAt(0).toUpperCase() + value.slice(1);

export const Component: React.FC = () => {
  const navigate = useNavigate();
  const { id } = useParams();
  const ruleId = Number(id);
  const isEdit = id !== undefined;
  const queryClient = useQueryClient();
  const { showSuccess } = useSnackbar();

  const [serverError, setServerError] = useState<ServerError | null>(null);
  const [pendingUpdate, setPendingUpdate] = useState<AutomationRuleInput | null>(null);
  const [addMenu, setAddMenu] = useState<HTMLElement | null>(null);
  const [dryRunOpen, setDryRunOpen] = useState(false);

  const methods = useForm<RuleFormValues>({
    resolver: zodResolver(ruleFormSchema),
    defaultValues: emptyFormValues(),
  });
  const { control, handleSubmit, reset, setValue, getValues, trigger, formState } = methods;
  const { fields, append, remove, move } = useFieldArray({ control, name: 'steps' });

  const entity = useWatch({ control, name: 'entity_type' });
  const triggerType = useWatch({ control, name: 'trigger_type' });
  const watchedSteps = useWatch({ control, name: 'steps' });

  const { data: rule, isLoading, isError } = useQuery({
    queryKey: automationKeys.rule(ruleId),
    queryFn: () => automationApi.getRule(ruleId),
    enabled: isEdit,
  });

  // The stored rule (also the one an update puts in the cache) is the form's
  // starting point; a save re-bases the form on the new version.
  useEffect(() => {
    if (rule) {
      reset(fromRule(rule));
    }
  }, [rule, reset]);

  const reportServerError = (error: unknown) => {
    const message = getAPIError(error)?.message ?? 'The rule could not be saved';
    setServerError(parseStepError(message));
  };

  const createMutation = useMutation({
    mutationFn: (payload: AutomationRuleInput) => automationApi.createRule(payload),
    onSuccess: async (created) => {
      await afterRuleCreated(queryClient, created);
      showSuccess('Rule created');
      navigate('/automation/rules');
    },
    onError: reportServerError,
  });

  const updateMutation = useMutation({
    mutationFn: (payload: AutomationRuleInput) => automationApi.updateRule(ruleId, payload),
    onSuccess: async (updated) => {
      await afterRuleChanged(queryClient, updated);
      showSuccess('Rule saved');
    },
    onError: reportServerError,
  });

  const onValid = (values: RuleFormValues) => {
    if (isEdit) {
      setPendingUpdate(toRulePayload(values, 'edit'));
    } else {
      createMutation.mutate(toRulePayload(values, 'create'));
    }
  };

  const onSubmit = (event: React.FormEvent<HTMLFormElement>) => {
    setServerError(null);
    void handleSubmit(onValid)(event);
  };

  const changeEntity = (next: AutomationEntityType) => {
    setValue('entity_type', next);
    setValue('trigger_type', AUTOMATION_TRIGGERS[next][0]);
    setValue('from', '');
    setValue('to', '');
    setValue('filter_value', '');
    getValues('steps').forEach((step, index) => {
      if (step.step_type !== 'condition') return;
      setValue(`steps.${index}`, {
        ...step,
        field: '',
        operator: '',
        value_text: '',
        value_list: [''],
        original_value: undefined,
        original_text: undefined,
        currency: '',
      });
    });
  };

  // Removing a jump target clears every branch that pointed at it and shows
  // the error on those conditions straight away.
  const removeStep = (index: number) => {
    const steps = getValues('steps');
    const removedKey = steps[index]?.key;
    const affected: number[] = [];
    steps.forEach((step, position) => {
      if (position !== index && step.step_type === 'condition' && step.else === 'jump' && step.else_target === removedKey) {
        setValue(`steps.${position}.else_target`, '');
        affected.push(position > index ? position - 1 : position);
      }
    });
    remove(index);
    if (affected.length > 0) {
      void trigger(affected.map((position) => `steps.${position}.else_target` as const));
    }
  };

  if (isEdit && isLoading) {
    return <Loading />;
  }
  if (isEdit && (isError || !rule)) {
    return <Alert severity="error">Rule not found</Alert>;
  }

  const statusWord = entity === 'lead' ? 'status' : 'stage';
  const valueOptions = entityValues(entity);
  const stepsError = formState.errors.steps?.root?.message ?? formState.errors.steps?.message;
  const atMax = fields.length >= AUTOMATION_MAX_STEPS;
  const saving = createMutation.isPending || updateMutation.isPending;

  const textField = (name: ScalarKey, label: string, multiline = false) => (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <TextField
          {...field}
          label={label}
          fullWidth
          multiline={multiline}
          minRows={multiline ? 2 : undefined}
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
        />
      )}
    />
  );

  const valueSelect = (name: 'from' | 'to' | 'filter_value', label: string) => (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <TextField
          select
          {...field}
          label={label}
          fullWidth
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
          slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
        >
          <MenuItem value="">Any</MenuItem>
          {valueOptions.map((value) => (
            <MenuItem key={value} value={value}>
              {valueLabel(entity, value)}
            </MenuItem>
          ))}
        </TextField>
      )}
    />
  );

  const numberField = (name: 'hour_utc' | 'stale_days', label: string) => (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <TextField
          inputRef={field.ref}
          name={field.name}
          onBlur={field.onBlur}
          value={Number.isNaN(field.value) ? '' : field.value}
          onChange={(event) => field.onChange(event.target.value === '' ? Number.NaN : Number(event.target.value))}
          type="number"
          label={label}
          fullWidth
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
        />
      )}
    />
  );

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3} gap={2} flexWrap="wrap">
        <Box>
          <Typography variant="h4">{isEdit ? 'Edit automation rule' : 'New automation rule'}</Typography>
          {isEdit && rule ? (
            <Typography variant="body2" color="text.secondary">
              Version {rule.version}
            </Typography>
          ) : null}
        </Box>
        {isEdit && rule ? (
          <Box textAlign="right">
            <Button variant="outlined" startIcon={<DryRunIcon />} onClick={() => setDryRunOpen(true)}>
              Dry run
            </Button>
            <Typography
              variant="caption"
              color="text.secondary"
              display="block"
              data-testid="dry-run-saved-version"
              sx={{ mt: 0.5 }}
            >
              {`Simulates the saved version ${rule.version}; save first to include unsaved changes.`}
            </Typography>
          </Box>
        ) : null}
      </Box>

      <FormProvider {...methods}>
        <form onSubmit={onSubmit} noValidate>
          <Paper sx={{ p: 3, mb: 3 }}>
            <Grid container spacing={2}>
              <Grid size={12}>{textField('name', 'Name')}</Grid>
              <Grid size={12}>{textField('description', 'Description', true)}</Grid>
              <Grid size={{ xs: 12, sm: 6 }}>
                <TextField
                  select
                  label="Entity type"
                  fullWidth
                  value={entity}
                  onChange={(event) => changeEntity(event.target.value as AutomationEntityType)}
                >
                  <MenuItem value="lead">Lead</MenuItem>
                  <MenuItem value="deal">Deal</MenuItem>
                </TextField>
              </Grid>
              <Grid size={{ xs: 12, sm: 6 }}>
                <Controller
                  control={control}
                  name="trigger_type"
                  render={({ field, fieldState }) => (
                    <TextField
                      select
                      {...field}
                      onChange={(event) => field.onChange(event.target.value as AutomationTriggerType)}
                      label="Trigger"
                      fullWidth
                      error={!!fieldState.error}
                      helperText={fieldState.error?.message}
                    >
                      {AUTOMATION_TRIGGERS[entity].map((value) => (
                        <MenuItem key={value} value={value}>
                          {triggerTypeLabel(value)}
                        </MenuItem>
                      ))}
                    </TextField>
                  )}
                />
              </Grid>
              {isChangeTrigger(triggerType) ? (
                <>
                  <Grid size={{ xs: 12, sm: 6 }}>{valueSelect('from', `From ${statusWord}`)}</Grid>
                  <Grid size={{ xs: 12, sm: 6 }}>{valueSelect('to', `To ${statusWord}`)}</Grid>
                </>
              ) : null}
              {triggerType === 'schedule' ? (
                <>
                  <Grid size={{ xs: 12, sm: 4 }}>{numberField('hour_utc', 'Hour (UTC)')}</Grid>
                  <Grid size={{ xs: 12, sm: 4 }}>{valueSelect('filter_value', `Only ${statusWord}`)}</Grid>
                  <Grid size={{ xs: 12, sm: 4 }}>{numberField('stale_days', 'Not updated for (days)')}</Grid>
                </>
              ) : null}
              {!isEdit ? (
                <Grid size={12}>
                  <Controller
                    control={control}
                    name="enabled"
                    render={({ field }) => (
                      <FormControlLabel
                        control={<Switch checked={field.value} onChange={(_event, checked) => field.onChange(checked)} />}
                        label="Enabled"
                      />
                    )}
                  />
                  <FormHelperText>New rules start disabled unless switched on</FormHelperText>
                </Grid>
              ) : null}
            </Grid>
          </Paper>

          <Typography variant="h6" sx={{ mb: 2 }}>
            Steps
          </Typography>
          <Stack spacing={2} sx={{ mb: 2 }}>
            {fields.map((field, index) => {
              const stepType = watchedSteps?.[index]?.step_type ?? field.step_type;
              const number = index + 1;
              return (
                <Paper key={field.key} role="group" aria-label={`Step ${number}`} sx={{ p: 2 }} variant="outlined">
                  <Box display="flex" alignItems="center" justifyContent="space-between" mb={2}>
                    <Typography variant="subtitle1">{`Step ${number} · ${stepTypeLabel(stepType)}`}</Typography>
                    <Box>
                      <IconButton
                        aria-label={`Move step ${number} up`}
                        disabled={index === 0}
                        onClick={() => move(index, index - 1)}
                      >
                        <UpIcon />
                      </IconButton>
                      <IconButton
                        aria-label={`Move step ${number} down`}
                        disabled={index === fields.length - 1}
                        onClick={() => move(index, index + 1)}
                      >
                        <DownIcon />
                      </IconButton>
                      <IconButton
                        aria-label={`Remove step ${number}`}
                        disabled={fields.length <= 1}
                        onClick={() => removeStep(index)}
                      >
                        <DeleteIcon />
                      </IconButton>
                    </Box>
                  </Box>
                  {serverError?.step === number ? (
                    <Alert severity="error" data-testid="step-server-error" sx={{ mb: 2 }}>
                      {serverError.text}
                    </Alert>
                  ) : null}
                  <RuleStepFields index={index} step={{ step_type: stepType }} entity={entity} />
                </Paper>
              );
            })}
          </Stack>

          <Box mb={3}>
            <Button
              variant="outlined"
              startIcon={<AddIcon />}
              disabled={atMax}
              onClick={(event) => setAddMenu(event.currentTarget)}
              aria-haspopup="menu"
            >
              Add step
            </Button>
            <Menu anchorEl={addMenu} open={addMenu !== null} onClose={() => setAddMenu(null)}>
              {AUTOMATION_STEP_TYPES.map((type) => (
                <MenuItem
                  key={type}
                  onClick={() => {
                    append(emptyStep(type));
                    setAddMenu(null);
                  }}
                >
                  {stepTypeLabel(type)}
                </MenuItem>
              ))}
            </Menu>
            {atMax ? <FormHelperText>A rule has at most 20 steps</FormHelperText> : null}
            {stepsError && !(atMax && stepsError === 'A rule has at most 20 steps') ? (
              <FormHelperText error>{stepsError}</FormHelperText>
            ) : null}
          </Box>

          {serverError && serverError.step === undefined ? (
            <Alert severity="error" data-testid="rule-server-error" sx={{ mb: 2 }}>
              {serverError.text}
            </Alert>
          ) : null}
          {serverError?.step !== undefined && serverError.step > fields.length ? (
            <Alert severity="error" data-testid="rule-server-error" sx={{ mb: 2 }}>
              {`Step ${serverError.step}: ${serverError.text}`}
            </Alert>
          ) : null}

          <Box display="flex" gap={2} justifyContent="flex-end">
            <Button onClick={() => navigate('/automation/rules')}>Cancel</Button>
            <Button type="submit" variant="contained" disabled={saving}>
              {isEdit ? 'Save changes' : 'Create rule'}
            </Button>
          </Box>
        </form>
      </FormProvider>

      <ConfirmDialog
        open={pendingUpdate !== null}
        title="Save rule changes?"
        message={`Saving creates version ${(rule?.version ?? 0) + 1} and cancels this rule's pending, waiting and running runs.`}
        confirmText="Save"
        onConfirm={() => {
          if (pendingUpdate) updateMutation.mutate(pendingUpdate);
          setPendingUpdate(null);
        }}
        onCancel={() => setPendingUpdate(null)}
      />

      {isEdit && rule ? <DryRunDialog rule={rule} open={dryRunOpen} onClose={() => setDryRunOpen(false)} /> : null}
    </Box>
  );
};
