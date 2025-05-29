import React from 'react';
import { TextField, type TextFieldProps } from '@mui/material';
import { Controller, useFormContext } from 'react-hook-form';
import { format } from 'date-fns';

type FormDatePickerProps = {
  name: string;
  label: string;
  minDate?: Date;
  maxDate?: Date;
} & Omit<TextFieldProps, 'name' | 'type'>;

export const FormDatePicker: React.FC<FormDatePickerProps> = ({
  name,
  label,
  minDate,
  maxDate,
  ...props
}) => {
  const { control } = useFormContext();

  return (
    <Controller
      name={name}
      control={control}
      render={({ field: { value, onChange, ...field }, fieldState: { error } }) => (
        <TextField
          {...field}
          {...props}
          type="date"
          label={label}
          value={value ? format(new Date(value), 'yyyy-MM-dd') : ''}
          onChange={(e) => onChange(e.target.value)}
          error={!!error}
          helperText={error?.message || props.helperText}
          fullWidth={props.fullWidth ?? true}
          InputLabelProps={{
            shrink: true,
            ...props.InputLabelProps,
          }}
          inputProps={{
            min: minDate ? format(minDate, 'yyyy-MM-dd') : undefined,
            max: maxDate ? format(maxDate, 'yyyy-MM-dd') : undefined,
            ...props.inputProps,
          }}
        />
      )}
    />
  );
};