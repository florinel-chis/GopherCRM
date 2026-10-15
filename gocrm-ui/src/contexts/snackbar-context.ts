import { createContext } from 'react';

export interface SnackbarContextType {
  showSuccess: (message: string) => void;
  showError: (message: string) => void;
  showWarning: (message: string) => void;
  showInfo: (message: string) => void;
}

export const SnackbarContext = createContext<SnackbarContextType | undefined>(undefined);
