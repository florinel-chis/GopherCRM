import { createTheme } from '@mui/material/styles';
import type { ThemeOptions } from '@mui/material/styles';
import { DEFAULT_PRIMARY_COLOR, isDefaultPrimaryColor, resolvePrimaryColor } from './primaryColor';

export { DEFAULT_PRIMARY_COLOR, resolvePrimaryColor } from './primaryColor';

const themeOptions: ThemeOptions = {
  palette: {
    mode: 'light',
    primary: {
      main: '#1976d2',
      light: '#42a5f5',
      dark: '#1565c0',
    },
    secondary: {
      main: '#dc004e',
      light: '#e33371',
      dark: '#9a0036',
    },
    background: {
      default: '#f5f5f5',
      paper: '#ffffff',
    },
    success: {
      main: '#4caf50',
    },
    warning: {
      main: '#ff9800',
    },
    error: {
      main: '#f44336',
    },
  },
  typography: {
    fontFamily: [
      '-apple-system',
      'BlinkMacSystemFont',
      '"Segoe UI"',
      'Roboto',
      '"Helvetica Neue"',
      'Arial',
      'sans-serif',
      '"Apple Color Emoji"',
      '"Segoe UI Emoji"',
      '"Segoe UI Symbol"',
    ].join(','),
    h1: {
      fontSize: '2.5rem',
      fontWeight: 500,
    },
    h2: {
      fontSize: '2rem',
      fontWeight: 500,
    },
    h3: {
      fontSize: '1.75rem',
      fontWeight: 500,
    },
    h4: {
      fontSize: '1.5rem',
      fontWeight: 500,
    },
    h5: {
      fontSize: '1.25rem',
      fontWeight: 500,
    },
    h6: {
      fontSize: '1rem',
      fontWeight: 500,
    },
  },
  shape: {
    borderRadius: 8,
  },
  components: {
    MuiButton: {
      styleOverrides: {
        root: {
          textTransform: 'none',
          fontWeight: 500,
        },
      },
    },
    MuiAppBar: {
      defaultProps: {
        elevation: 0,
      },
      styleOverrides: {
        root: {
          borderBottom: '1px solid',
          borderBottomColor: 'rgba(0, 0, 0, 0.12)',
        },
      },
    },
    MuiDrawer: {
      styleOverrides: {
        paper: {
          borderRight: '1px solid',
          borderRightColor: 'rgba(0, 0, 0, 0.12)',
        },
      },
    },
    MuiCard: {
      defaultProps: {
        elevation: 1,
      },
    },
    MuiTableCell: {
      styleOverrides: {
        head: {
          fontWeight: 600,
        },
      },
    },
  },
};

/**
 * The light theme with the given primary colour. The colour is validated
 * again (resolvePrimaryColor), so an unsupported value can never reach
 * createTheme. The default keeps its hand-picked light and dark variants; any
 * other colour passes only `main`, so MUI computes light, dark and
 * contrastText from it instead of keeping the default's stale variants.
 */
export const createAppTheme = (colour: string = DEFAULT_PRIMARY_COLOR) => {
  const main = resolvePrimaryColor(colour);
  const primary = isDefaultPrimaryColor(main)
    ? { main: DEFAULT_PRIMARY_COLOR, light: '#42a5f5', dark: '#1565c0' }
    : { main };
  return createTheme({
    ...themeOptions,
    palette: { ...themeOptions.palette, primary },
  });
};

export const theme = createAppTheme();

// Dark theme variant
export const darkTheme = createTheme({
  ...themeOptions,
  palette: {
    ...themeOptions.palette,
    mode: 'dark',
    background: {
      default: '#121212',
      paper: '#1e1e1e',
    },
    primary: {
      main: '#90caf9',
      light: '#bbdefb',
      dark: '#42a5f5',
    },
  },
});