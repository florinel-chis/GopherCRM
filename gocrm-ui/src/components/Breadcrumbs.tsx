import React from 'react';
import { useLocation, Link as RouterLink } from 'react-router-dom';
import {
  Breadcrumbs as MuiBreadcrumbs,
  Link,
  Typography,
  Box,
} from '@mui/material';
import { NavigateNext as NavigateNextIcon } from '@mui/icons-material';

interface BreadcrumbItem {
  label: string;
  path?: string;
}

const routeLabels: Record<string, string> = {
  dashboard: 'Dashboard',
  leads: 'Leads',
  customers: 'Customers',
  tickets: 'Tickets',
  tasks: 'Tasks',
  users: 'Users',
  profile: 'Profile',
  settings: 'Settings',
  apikeys: 'API Keys',
  new: 'New',
  edit: 'Edit',
};

export const Breadcrumbs: React.FC = () => {
  const location = useLocation();
  const pathnames = location.pathname.split('/').filter((x) => x);

  const breadcrumbs: BreadcrumbItem[] = [
    { label: 'Dashboard', path: '/' },
  ];

  let path = '';
  pathnames.forEach((segment, index) => {
    path += `/${segment}`;
    
    // Skip ID segments (numbers)
    if (!isNaN(Number(segment))) {
      return;
    }

    const label = routeLabels[segment] || segment.charAt(0).toUpperCase() + segment.slice(1);
    const isLast = index === pathnames.length - 1;

    breadcrumbs.push({
      label,
      path: isLast ? undefined : path,
    });
  });

  // Don't show breadcrumbs on dashboard
  if (pathnames.length === 0) {
    return null;
  }

  return (
    <Box mb={2}>
      <MuiBreadcrumbs
        separator={<NavigateNextIcon fontSize="small" />}
        aria-label="breadcrumb"
      >
        {breadcrumbs.map((breadcrumb, index) => {
          const isLast = index === breadcrumbs.length - 1;

          if (isLast || !breadcrumb.path) {
            return (
              <Typography key={index} color="text.primary">
                {breadcrumb.label}
              </Typography>
            );
          }

          return (
            <Link
              key={index}
              component={RouterLink}
              to={breadcrumb.path}
              color="inherit"
              underline="hover"
            >
              {breadcrumb.label}
            </Link>
          );
        })}
      </MuiBreadcrumbs>
    </Box>
  );
};