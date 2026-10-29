import React from 'react';
import { Box, Button, FormHelperText, IconButton, Stack, TextField } from '@mui/material';
import { Add as AddIcon, RemoveCircleOutline as RemoveIcon } from '@mui/icons-material';

export interface StringListFieldProps {
  value: string[];
  onChange: (value: string[]) => void;
  error?: boolean;
  helperText?: string;
}

/**
 * Editor of an in / not-in list, one field per item. Items are kept exactly
 * as typed: commas, surrounding spaces and empty strings are all values the
 * API compares against, so nothing is split or trimmed.
 */
export const StringListField: React.FC<StringListFieldProps> = ({ value, onChange, error, helperText }) => {
  const items = value.length > 0 ? value : [''];

  const setItem = (index: number, text: string) =>
    onChange(items.map((item, i) => (i === index ? text : item)));

  return (
    <Box>
      <Stack spacing={1}>
        {items.map((item, index) => (
          // Items have no identity beyond their position.
          <Box key={index} sx={{ display: 'flex', gap: 1, alignItems: 'center' }}>
            <TextField
              size="small"
              fullWidth
              label={`Value ${index + 1}`}
              value={item}
              error={error}
              onChange={(event) => setItem(index, event.target.value)}
            />
            <IconButton
              aria-label={`Remove value ${index + 1}`}
              disabled={items.length <= 1}
              onClick={() => onChange(items.filter((_, i) => i !== index))}
            >
              <RemoveIcon />
            </IconButton>
          </Box>
        ))}
      </Stack>
      <Button size="small" startIcon={<AddIcon />} sx={{ mt: 1 }} onClick={() => onChange([...items, ''])}>
        Add value
      </Button>
      {helperText ? <FormHelperText error={error}>{helperText}</FormHelperText> : null}
    </Box>
  );
};
