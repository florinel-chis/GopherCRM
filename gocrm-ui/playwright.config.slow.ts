import { defineConfig, devices } from '@playwright/test';
import baseConfig from './playwright.config';

/**
 * Slower configuration for debugging timing issues
 */
export default defineConfig({
  ...baseConfig,
  
  /* Even slower timeouts for debugging */
  timeout: 60 * 1000, // 60 seconds per test
  
  use: {
    ...baseConfig.use,
    /* Slower action timeouts */
    actionTimeout: 20 * 1000,
    navigationTimeout: 60 * 1000,
    
    /* Always record video for debugging */
    video: 'on',
    
    /* Slow down actions */
    launchOptions: {
      slowMo: 500, // 500ms between actions
    },
  },
});