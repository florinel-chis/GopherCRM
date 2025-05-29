import React from 'react';
import {
  FormControlLabel,
  Switch,
  type SwitchProps,
  FormHelperText,
  Box,
} from '@mui/material';
import { Controller, useFormContext } from 'react-hook-form';

type FormSwitchProps = {
  name: string;
  label: string;
} & Omit<SwitchProps, 'name'>;

export const FormSwitch: React.FC<FormSwitchProps> = ({ name, label, ...props }) => {
  const { control } = useFormContext();

  return (
    <Controller
      name={name}
      control={control}
      render={({ field: { value, onChange, ...field }, fieldState: { error } }) => (
        <Box>
          <FormControlLabel
            control={
              <Switch
                {...field}
                {...props}
                checked={!!value}
                onChange={(e) => onChange(e.target.checked)}
              />
            }
            label={label}
          />
          {error && (
            <FormHelperText error>{error.message}</FormHelperText>
          )}
        </Box>
      )}
    />
  );
};