import React from 'react';
import {
  Box,
  Chip,
  FormControl,
  FormControlLabel,
  InputLabel,
  MenuItem,
  Select,
  Switch,
  TextField,
} from '@mui/material';
import type { Configuration, ConfigurationValue } from '@/api/endpoints/configurations';
import { parseValidValues } from './configValueDraft';

interface ConfigValueEditorProps {
  config: Configuration;
  /** The value of a boolean, select, string or sensitive editor. */
  value: ConfigurationValue;
  onValueChange: (value: ConfigurationValue) => void;
  /** The raw text of a JSON, free-form array or number editor, parsed on Save. */
  draft: string;
  onDraftChange: (draft: string) => void;
  /** The field error of the last Save, if it was refused. */
  error?: string;
}

/** The value field of the edit dialog on Settings > Configuration. */
export const ConfigValueEditor: React.FC<ConfigValueEditorProps> = ({
  config,
  value,
  onValueChange,
  draft,
  onDraftChange,
  error,
}) => {
  if (config.is_sensitive) {
    return (
      <TextField
        fullWidth
        type="password"
        autoComplete="new-password"
        label="New value"
        placeholder="enter new value"
        value={value}
        onChange={(e) => onValueChange(e.target.value)}
        helperText="Stored encrypted and never shown again. Save an empty value to clear it."
      />
    );
  }

  const validValues = parseValidValues(config);
  const draftField = (props: React.ComponentProps<typeof TextField>) => (
    <TextField
      fullWidth
      label="Value"
      value={draft}
      onChange={(e) => onDraftChange(e.target.value)}
      error={Boolean(error)}
      helperText={error}
      {...props}
    />
  );

  switch (config.type) {
    case 'boolean':
      return (
        <FormControlLabel
          control={<Switch checked={value === true} onChange={(e) => onValueChange(e.target.checked)} />}
          label={value ? 'True' : 'False'}
        />
      );

    case 'integer':
      return draftField({ type: 'number', inputProps: { step: 1 } });

    case 'float':
      return draftField({ type: 'number', inputProps: { step: 0.1 } });

    case 'array':
      if (validValues) {
        return (
          <FormControl fullWidth>
            <InputLabel>Select Values</InputLabel>
            <Select
              multiple
              value={Array.isArray(value) ? value.map(String) : []}
              onChange={(e) => onValueChange(e.target.value)}
              renderValue={(selected) => (
                <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
                  {(selected as string[]).map((item) => (
                    <Chip key={item} label={item} size="small" />
                  ))}
                </Box>
              )}
            >
              {validValues.map((option) => (
                <MenuItem key={String(option)} value={String(option)}>
                  {String(option)}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        );
      }
      return draftField({ multiline: true, rows: 3, placeholder: 'Enter JSON array' });

    case 'json':
      return draftField({ multiline: true, rows: 4, placeholder: 'Enter JSON' });

    default:
      if (validValues) {
        return (
          <FormControl fullWidth>
            <InputLabel>Select Value</InputLabel>
            <Select value={value} onChange={(e) => onValueChange(e.target.value)}>
              {validValues.map((option) => (
                <MenuItem key={String(option)} value={String(option)}>
                  {String(option)}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        );
      }
      return (
        <TextField fullWidth label="Value" value={value} onChange={(e) => onValueChange(e.target.value)} />
      );
  }
};
