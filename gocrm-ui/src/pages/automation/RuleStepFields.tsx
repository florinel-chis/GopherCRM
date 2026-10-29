import React from 'react';
import { Controller, useFormContext, useWatch } from 'react-hook-form';
import { Autocomplete, Grid, MenuItem, TextField } from '@mui/material';
import {
  AUTOMATION_CONDITION_FIELDS,
  AUTOMATION_PLACEHOLDERS,
  AUTOMATION_TASK_PLACEHOLDERS,
  type AutomationEntityType,
} from '@/types';
import { operatorLabel, stepTypeLabel } from './automationFormat';
import {
  CONDITION_FIELD_LABELS,
  WAIT_UNIT_LABELS,
  WAIT_UNITS,
  conditionFieldKind,
  entityValues,
  isListOperator,
  isValuelessOperator,
  operatorsFor,
  placeholderHelp,
  type RuleFormValues,
  type StepFormValues,
} from './ruleFormSchema';
import { StaffUserAutocomplete } from './StaffUserAutocomplete';
import { StringListField } from './StringListField';

type TextKey = 'title' | 'description' | 'subject' | 'body' | 'value_text' | 'currency';
type NumberKey = 'wait_amount' | 'due_in_days';
type SelectKey = 'wait_unit' | 'field' | 'operator' | 'value_text' | 'else' | 'else_target' | 'recipient' | 'priority';

interface Option {
  value: string;
  label: string;
  disabled?: boolean;
}

const RECIPIENT_OPTIONS: Option[] = [
  { value: 'owner', label: 'Record owner' },
  { value: 'user', label: 'Specific user' },
];

const PRIORITY_OPTIONS: Option[] = [
  { value: '', label: 'None' },
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
];

// react-hook-form focuses the first invalid field on a timer. An MUI Select's
// imperative handle throws if the select unmounted meanwhile (a removed step),
// so focus goes through a guard.
interface SelectHandle {
  node?: HTMLElement | null;
  focus?: () => void;
}
const guardedSelectRef =
  (ref: (instance: unknown) => void) =>
  (instance: SelectHandle | null): void =>
    ref(
      instance
        ? {
            focus: () => {
              if (instance.node?.isConnected) instance.focus?.();
            },
          }
        : null,
    );


function StepTextField({
  index,
  name,
  label,
  helperText,
  multiline = false,
}: {
  index: number;
  name: TextKey;
  label: string;
  helperText?: string;
  multiline?: boolean;
}) {
  const { control } = useFormContext<RuleFormValues>();
  return (
    <Controller
      control={control}
      name={`steps.${index}.${name}`}
      render={({ field, fieldState }) => (
        <TextField
          {...field}
          label={label}
          size="small"
          fullWidth
          multiline={multiline}
          minRows={multiline ? 2 : undefined}
          error={!!fieldState.error}
          helperText={fieldState.error?.message ?? helperText}
        />
      )}
    />
  );
}

function StepNumberField({ index, name, label }: { index: number; name: NumberKey; label: string }) {
  const { control } = useFormContext<RuleFormValues>();
  return (
    <Controller
      control={control}
      name={`steps.${index}.${name}`}
      render={({ field, fieldState }) => (
        <TextField
          inputRef={field.ref}
          name={field.name}
          onBlur={field.onBlur}
          value={Number.isNaN(field.value) ? '' : field.value}
          onChange={(event) => field.onChange(event.target.value === '' ? Number.NaN : Number(event.target.value))}
          type="number"
          label={label}
          size="small"
          fullWidth
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
        />
      )}
    />
  );
}

function StepSelectField({
  index,
  name,
  label,
  options,
  onSelect,
}: {
  index: number;
  name: SelectKey;
  label: string;
  options: Option[];
  onSelect?: (value: string) => void;
}) {
  const { control } = useFormContext<RuleFormValues>();
  return (
    <Controller
      control={control}
      name={`steps.${index}.${name}`}
      render={({ field, fieldState }) => (
        <TextField
          select
          inputRef={guardedSelectRef(field.ref)}
          name={field.name}
          onBlur={field.onBlur}
          value={options.some((option) => option.value === field.value) ? field.value : ''}
          onChange={(event) => (onSelect ? onSelect(event.target.value) : field.onChange(event.target.value))}
          label={label}
          size="small"
          fullWidth
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
          slotProps={{ select: { displayEmpty: true }, inputLabel: { shrink: true } }}
        >
          {options.map((option) => (
            <MenuItem key={option.value} value={option.value} disabled={option.disabled}>
              {option.label}
            </MenuItem>
          ))}
        </TextField>
      )}
    />
  );
}

// Status / stage value. The API compares these as plain strings and checks
// none of them against the status or stage list (a saved "contains qual" or a
// retired status is valid), so the input takes any text, kept verbatim, and
// merely suggests the known values.
function StepSuggestedTextField({ index, options }: { index: number; options: readonly string[] }) {
  const { control } = useFormContext<RuleFormValues>();
  return (
    <Controller
      control={control}
      name={`steps.${index}.value_text`}
      render={({ field, fieldState }) => (
        <Autocomplete<string, false, true, true>
          freeSolo
          disableClearable
          options={[...options]}
          value={field.value}
          inputValue={field.value}
          onInputChange={(_event, text) => field.onChange(text)}
          onChange={(_event, text) => field.onChange(text ?? '')}
          filterOptions={(candidates) => candidates}
          renderInput={(params) => (
            <TextField
              {...params}
              onBlur={field.onBlur}
              label="Value"
              size="small"
              error={!!fieldState.error}
              helperText={fieldState.error?.message}
            />
          )}
        />
      )}
    />
  );
}

function StepUserField({ index, label }: { index: number; label: string }) {
  const { control } = useFormContext<RuleFormValues>();
  return (
    <Controller
      control={control}
      name={`steps.${index}.user_id`}
      render={({ field, fieldState }) => (
        <StaffUserAutocomplete
          label={label}
          value={field.value > 0 ? field.value : null}
          onChange={(id) => field.onChange(id ?? 0)}
          error={!!fieldState.error}
          helperText={fieldState.error?.message}
        />
      )}
    />
  );
}

function RecipientFields({ index, label }: { index: number; label: string }) {
  const { control } = useFormContext<RuleFormValues>();
  const recipient = useWatch({ control, name: `steps.${index}.recipient` });
  return (
    <>
      <Grid size={{ xs: 12, sm: 6 }}>
        <StepSelectField index={index} name="recipient" label={label} options={RECIPIENT_OPTIONS} />
      </Grid>
      {recipient === 'user' ? (
        <Grid size={{ xs: 12, sm: 6 }}>
          <StepUserField index={index} label="User" />
        </Grid>
      ) : null}
    </>
  );
}

function ConditionFields({ index, entity }: { index: number; entity: AutomationEntityType }) {
  const { control, setValue, getValues } = useFormContext<RuleFormValues>();
  const step = useWatch({ control, name: `steps.${index}` });
  const steps = useWatch({ control, name: 'steps' });
  const kind = conditionFieldKind(entity, step.field);

  const fieldOptions: Option[] = Object.keys(AUTOMATION_CONDITION_FIELDS[entity]).map((field) => ({
    value: field,
    label: CONDITION_FIELD_LABELS[field] ?? field,
  }));
  const operatorOptions: Option[] = operatorsFor(kind).map((operator) => ({
    value: operator,
    label: operatorLabel(operator),
  }));

  // Later steps, valued by their stable key so a reorder never re-targets the
  // branch. A target that moved before the condition stays visible (disabled)
  // next to its error.
  const targetOptions: Option[] = steps
    .map((candidate, position) => ({
      value: candidate.key,
      label: `Step ${position + 1} · ${stepTypeLabel(candidate.step_type)}`,
      disabled: position <= index,
    }))
    .filter((option, position) => position > index || (option.value === step.else_target && position !== index));

  const changeField = (field: string) => {
    setValue(`steps.${index}.field`, field);
    setValue(`steps.${index}.operator`, '');
    setValue(`steps.${index}.value_text`, '');
    setValue(`steps.${index}.value_list`, ['']);
    setValue(`steps.${index}.original_value`, undefined);
    setValue(`steps.${index}.original_text`, undefined);
    setValue(`steps.${index}.currency`, '');
  };

  const changeElse = (value: string) => {
    setValue(`steps.${index}.else`, value === 'jump' ? 'jump' : 'stop');
    if (value === 'jump' && !getValues(`steps.${index}.else_target`)) {
      const next = getValues('steps')[index + 1];
      setValue(`steps.${index}.else_target`, next ? next.key : '');
    }
  };

  // eq / neq on status or stage suggest the known values; contains is free text.
  const suggested =
    kind === 'string' && (step.field === 'status' || step.field === 'stage') && step.operator !== 'contains';
  const listOperator = isListOperator(step.operator);
  const valueless = isValuelessOperator(step.operator) || !step.operator;

  return (
    <>
      <Grid size={{ xs: 12, sm: 4 }}>
        <StepSelectField index={index} name="field" label="Field" options={fieldOptions} onSelect={changeField} />
      </Grid>
      <Grid size={{ xs: 12, sm: 4 }}>
        <StepSelectField index={index} name="operator" label="Operator" options={operatorOptions} />
      </Grid>
      {!valueless && !listOperator ? (
        <Grid size={{ xs: 12, sm: 4 }}>
          {suggested ? (
            <StepSuggestedTextField index={index} options={entityValues(entity)} />
          ) : (
            <StepTextField
              index={index}
              name="value_text"
              label={kind === 'money' ? 'Amount' : 'Value'}
              helperText={kind === 'money' ? 'Major units, e.g. 1500 or -12.50' : undefined}
            />
          )}
        </Grid>
      ) : null}
      {listOperator ? (
        <Grid size={12}>
          <Controller
            control={control}
            name={`steps.${index}.value_list`}
            render={({ field, fieldState }) => (
              <StringListField
                value={field.value}
                onChange={field.onChange}
                error={!!fieldState.error}
                helperText={fieldState.error?.message}
              />
            )}
          />
        </Grid>
      ) : null}
      {kind === 'money' ? (
        <Grid size={{ xs: 12, sm: 4 }}>
          <StepTextField index={index} name="currency" label="Currency" />
        </Grid>
      ) : null}
      <Grid size={{ xs: 12, sm: 4 }}>
        <StepSelectField
          index={index}
          name="else"
          label="If false"
          options={[
            { value: 'stop', label: 'Stop the run' },
            { value: 'jump', label: 'Jump to step' },
          ]}
          onSelect={changeElse}
        />
      </Grid>
      {step.else === 'jump' ? (
        <Grid size={{ xs: 12, sm: 4 }}>
          <StepSelectField index={index} name="else_target" label="Jump to step" options={targetOptions} />
        </Grid>
      ) : null}
    </>
  );
}

export interface RuleStepFieldsProps {
  index: number;
  step: Pick<StepFormValues, 'step_type'>;
  entity: AutomationEntityType;
}

/** The per-type fields of one step card. */
export const RuleStepFields: React.FC<RuleStepFieldsProps> = ({ index, step, entity }) => {
  const taskHelp = placeholderHelp(AUTOMATION_TASK_PLACEHOLDERS[entity]);
  const fullHelp = placeholderHelp(AUTOMATION_PLACEHOLDERS[entity]);

  switch (step.step_type) {
    case 'wait':
      return (
        <Grid container spacing={2}>
          <Grid size={{ xs: 12, sm: 4 }}>
            <StepNumberField index={index} name="wait_amount" label="Duration" />
          </Grid>
          <Grid size={{ xs: 12, sm: 4 }}>
            <StepSelectField
              index={index}
              name="wait_unit"
              label="Unit"
              options={WAIT_UNITS.map((unit) => ({ value: unit, label: WAIT_UNIT_LABELS[unit] }))}
            />
          </Grid>
        </Grid>
      );
    case 'condition':
      return (
        <Grid container spacing={2}>
          <ConditionFields index={index} entity={entity} />
        </Grid>
      );
    case 'create_task':
      return (
        <Grid container spacing={2}>
          <Grid size={12}>
            <StepTextField index={index} name="title" label="Title" helperText={taskHelp} />
          </Grid>
          <Grid size={12}>
            <StepTextField index={index} name="description" label="Description" helperText={taskHelp} multiline />
          </Grid>
          <Grid size={{ xs: 12, sm: 6 }}>
            <StepSelectField index={index} name="priority" label="Priority" options={PRIORITY_OPTIONS} />
          </Grid>
          <Grid size={{ xs: 12, sm: 6 }}>
            <StepNumberField index={index} name="due_in_days" label="Due in days" />
          </Grid>
          <RecipientFields index={index} label="Assign to" />
        </Grid>
      );
    case 'assign_owner':
      return (
        <Grid container spacing={2}>
          <Grid size={{ xs: 12, sm: 6 }}>
            <StepUserField index={index} label="New owner" />
          </Grid>
        </Grid>
      );
    case 'send_email':
      return (
        <Grid container spacing={2}>
          <RecipientFields index={index} label="Send to" />
          <Grid size={12}>
            <StepTextField index={index} name="subject" label="Subject" helperText={fullHelp} />
          </Grid>
          <Grid size={12}>
            <StepTextField index={index} name="body" label="Body" helperText={fullHelp} multiline />
          </Grid>
        </Grid>
      );
    case 'notify':
      return (
        <Grid container spacing={2}>
          <RecipientFields index={index} label="Notify" />
          <Grid size={12}>
            <StepTextField index={index} name="title" label="Title" helperText={fullHelp} />
          </Grid>
          <Grid size={12}>
            <StepTextField index={index} name="body" label="Body" helperText={fullHelp} multiline />
          </Grid>
        </Grid>
      );
    default:
      return null;
  }
};
