import React from 'react';
import { TextField, type TextFieldProps } from '@mui/material';
import { Controller, useFormContext } from 'react-hook-form';

type FormTextFieldProps = {
  name: string;
} & Omit<TextFieldProps, 'name'>;

export const FormTextField: React.FC<FormTextFieldProps> = ({ name, ...props }) => {
  const { control } = useFormContext();

  return (
    <Controller
      name={name}
      control={control}
      render={({ field, fieldState: { error } }) => (
        <TextField
          {...field}
          {...props}
          error={!!error}
          helperText={error?.message || props.helperText}
          fullWidth={props.fullWidth ?? true}
        />
      )}
    />
  );
};