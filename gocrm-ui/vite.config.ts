/// <reference types="vitest" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'path';

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  // `make e2e` (scripts/e2e/run.sh) serves the release-style build, whose API
  // base is the relative /api/v1, with `vite preview` and sets E2E_API_ORIGIN
  // to the run's backend. The preview server then forwards /api/v1 there on the
  // web app's own origin, as the reverse proxy does in production. It affects
  // `vite preview` only, never the build output or the dev server.
  ...(process.env.E2E_API_ORIGIN
    ? { preview: { proxy: { '/api/v1': { target: process.env.E2E_API_ORIGIN } } } }
    : {}),
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    exclude: ['e2e/**/*', 'node_modules/**/*'],
  },
});
