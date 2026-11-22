import React, { useState } from 'react';
import { Box, Button, Chip, FormHelperText, Stack, TextField, Typography } from '@mui/material';

export interface StringListEditorProps {
  label: string;
  placeholder: string;
  values: string[];
  disabled?: boolean;
  error?: string;
  helperText?: string;
  onChange: (values: string[]) => void;
}

// Chip editor shared by the notification addresses, the allowed domains and the
// per-field dropdown options.
export const StringListEditor: React.FC<StringListEditorProps> = ({
  label,
  placeholder,
  values,
  disabled,
  error,
  helperText,
  onChange,
}) => {
  const [draft, setDraft] = useState('');

  const commit = () => {
    const value = draft.trim();
    if (!value || values.includes(value)) {
      setDraft('');
      return;
    }
    onChange([...values, value]);
    setDraft('');
  };

  return (
    <Box>
      <Stack direction="row" spacing={1} mb={1}>
        <TextField
          size="small"
          fullWidth
          label={label}
          placeholder={placeholder}
          value={draft}
          disabled={disabled}
          error={Boolean(error)}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              event.preventDefault();
              commit();
            }
          }}
        />
        <Button variant="outlined" onClick={commit} disabled={disabled} aria-label={`Add ${label}`}>
          Add
        </Button>
      </Stack>
      <Box display="flex" gap={1} flexWrap="wrap">
        {values.length === 0 ? (
          <Typography variant="body2" color="text.secondary">
            None yet.
          </Typography>
        ) : (
          values.map((value) => (
            <Chip
              key={value}
              label={value}
              onDelete={disabled ? undefined : () => onChange(values.filter((v) => v !== value))}
            />
          ))
        )}
      </Box>
      {(error || helperText) && (
        <FormHelperText error={Boolean(error)}>{error || helperText}</FormHelperText>
      )}
    </Box>
  );
};
