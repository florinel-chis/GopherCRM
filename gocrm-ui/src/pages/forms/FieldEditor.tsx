import React from 'react';
import { Controller, useFormContext } from 'react-hook-form';
import {
  Box,
  Card,
  CardContent,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  ArrowDownward as ArrowDownwardIcon,
  ArrowUpward as ArrowUpwardIcon,
  Delete as DeleteIcon,
} from '@mui/icons-material';
import { FormSwitch, FormTextField } from '@/components/form';
import { fieldTypes, slugify, type FormBuilderValues } from './formBuilderSchema';
import { StringListEditor } from './StringListEditor';

export interface FieldEditorProps {
  index: number;
  total: number;
  disabled: boolean;
  autoName: boolean;
  onManualName: () => void;
  onMove: (from: number, to: number) => void;
  onRemove: (index: number) => void;
}

export const FieldEditor: React.FC<FieldEditorProps> = ({
  index,
  total,
  disabled,
  autoName,
  onManualName,
  onMove,
  onRemove,
}) => {
  const { control, watch, setValue, getValues, formState } = useFormContext<FormBuilderValues>();
  const type = watch(`fields.${index}.type`);
  const options = watch(`fields.${index}.options`) || [];
  const optionsError = formState.errors.fields?.[index]?.options?.message;
  const position = index + 1;

  return (
    <Card variant="outlined" data-testid={`field-editor-${index}`}>
      <CardContent>
        <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
          <Typography variant="subtitle1">
            Field {position}: {fieldTypes.find((entry) => entry.value === type)?.label ?? type}
          </Typography>
          {!disabled && (
            <Box>
              <Tooltip title="Move up">
                <span>
                  <IconButton
                    size="small"
                    aria-label={`Move field ${position} up`}
                    disabled={index === 0}
                    onClick={() => onMove(index, index - 1)}
                  >
                    <ArrowUpwardIcon fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
              <Tooltip title="Move down">
                <span>
                  <IconButton
                    size="small"
                    aria-label={`Move field ${position} down`}
                    disabled={index === total - 1}
                    onClick={() => onMove(index, index + 1)}
                  >
                    <ArrowDownwardIcon fontSize="small" />
                  </IconButton>
                </span>
              </Tooltip>
              <Tooltip title="Remove field">
                <IconButton
                  size="small"
                  aria-label={`Remove field ${position}`}
                  onClick={() => onRemove(index)}
                >
                  <DeleteIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            </Box>
          )}
        </Box>

        <Stack spacing={2}>
          <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
            <Controller
              name={`fields.${index}.label`}
              control={control}
              render={({ field, fieldState }) => (
                <TextField
                  {...field}
                  label={`Field ${position} label`}
                  required
                  fullWidth
                  disabled={disabled}
                  error={Boolean(fieldState.error)}
                  helperText={fieldState.error?.message}
                  onChange={(event) => {
                    const label = event.target.value;
                    field.onChange(label);
                    // The machine name tracks the label until it is edited by
                    // hand, then it is left alone. The email field is exempt:
                    // the server insists it is named "email".
                    if (autoName && getValues(`fields.${index}.type`) !== 'email') {
                      setValue(`fields.${index}.name`, slugify(label));
                    }
                  }}
                />
              )}
            />
            <Controller
              name={`fields.${index}.name`}
              control={control}
              render={({ field, fieldState }) => (
                <TextField
                  {...field}
                  label={`Field ${position} name`}
                  required
                  fullWidth
                  disabled={disabled}
                  error={Boolean(fieldState.error)}
                  helperText={
                    fieldState.error?.message ||
                    'Machine key stored with every submission; reserved names first_name, last_name, email, phone, company and position map onto the lead.'
                  }
                  onChange={(event) => {
                    onManualName();
                    field.onChange(event.target.value);
                  }}
                />
              )}
            />
          </Box>

          <Box display="grid" gridTemplateColumns={{ xs: '1fr', md: '1fr 1fr' }} gap={2}>
            <FormTextField
              name={`fields.${index}.placeholder`}
              label={`Field ${position} placeholder`}
              disabled={disabled}
            />
            <FormTextField
              name={`fields.${index}.help_text`}
              label={`Field ${position} help text`}
              disabled={disabled}
            />
          </Box>

          {/* Hidden rather than unmounted: react-hook-form drops the value of an
              unmounted input at submit time, which turns a required schema key
              into a validation error with nothing on screen to fix. */}
          <Box sx={{ display: type === 'hidden' ? 'none' : 'block' }}>
            <FormSwitch
              name={`fields.${index}.required`}
              label={`Field ${position} required`}
              disabled={disabled}
            />
          </Box>

          {type === 'select' && (
            <StringListEditor
              label={`Field ${position} options`}
              placeholder="Under €10k"
              values={options}
              disabled={disabled}
              error={optionsError}
              onChange={(next) => setValue(`fields.${index}.options`, next, { shouldDirty: true })}
            />
          )}
        </Stack>
      </CardContent>
    </Card>
  );
};
