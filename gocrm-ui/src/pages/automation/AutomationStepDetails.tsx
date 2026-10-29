import { Box, Link, Typography } from '@mui/material';
import { Link as RouterLink } from 'react-router-dom';
import { DATE_FALLBACK } from '@/utils/date';
import { formatStepDetails } from './automationFormat';

// The ids and statuses a step recorded, as inline "label: value" pairs.
export function AutomationStepDetails({
  details,
}: {
  details: Record<string, unknown> | null | undefined;
}) {
  const items = formatStepDetails(details);
  if (items.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary" component="span">
        {DATE_FALLBACK}
      </Typography>
    );
  }
  return (
    <Box
      component="span"
      sx={{ display: 'inline-flex', flexWrap: 'wrap', columnGap: 2, rowGap: 0.5 }}
    >
      {items.map((item) => (
        <Typography key={item.key} variant="body2" component="span">
          <Box component="span" sx={{ color: 'text.secondary' }}>
            {item.label}:
          </Box>{' '}
          {item.href ? (
            <Link component={RouterLink} to={item.href}>
              {item.value}
            </Link>
          ) : (
            item.value
          )}
        </Typography>
      ))}
    </Box>
  );
}
