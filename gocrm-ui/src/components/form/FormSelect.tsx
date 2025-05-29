import React from 'react';
import {
  FormControl,
  FormHelperText,
  InputLabel,
  MenuItem,
  Select,
  type SelectProps,
} from '@mui/material';
import { Controller, useFormContext } from 'react-hook-form';

export type SelectOption = {
  value: string | number;
  label: string;
};

type FormSelectProps = {
  name: string;
  label: string;
  options: SelectOption[];
} & Omit<SelectProps, 'name' | 'label'>;

export const FormSelect: React.FC<FormSelectProps> = ({
  name,
  label,
  options,
  ...props
}) => {
  const { control } = useFormContext();

  return (
    <Controller
      name={name}
      control={control}
      render={({ field, fieldState: { error } }) => (
        <FormControl fullWidth={props.fullWidth ?? true} error={!!error}>
          <InputLabel id={`${name}-label`}>{label}</InputLabel>
          <Select
            {...field}
            {...props}
            labelId={`${name}-label`}
            label={label}
          >
            {options.map((option) => (
              <MenuItem key={option.value} value={option.value}>
                {option.label}
              </MenuItem>
            ))}
          </Select>
          {error && <FormHelperText>{error.message}</FormHelperText>}
        </FormControl>
      )}
    />
  );
};