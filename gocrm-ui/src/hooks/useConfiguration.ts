import { useContext } from 'react';
import { ConfigurationContext } from '@/contexts/configuration-context';

export const useConfiguration = () => {
  const context = useContext(ConfigurationContext);
  if (context === undefined) {
    throw new Error('useConfiguration must be used within a ConfigurationProvider');
  }
  return context;
};
