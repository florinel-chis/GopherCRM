/// <reference types="vitest" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import path from 'path';

// The header set of every web app page and asset, byte for byte the values in
// gocrm-ui/docker/nginx.conf.
const webAppHeaders = {
  'Content-Security-Policy':
    "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
  'X-Frame-Options': 'DENY',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'same-origin',
};

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  // `vite preview` sends the browser headers of the production nginx config
  // (gocrm-ui/docker/nginx.conf), so the release browser tests run with them
  // on. Vite applies them to static files and the SPA fallback, not to proxied
  // API answers. The dev server (`server.headers`) deliberately sends none: it
  // injects an inline React refresh preamble that this content policy blocks.
  //
  // `make e2e` (scripts/e2e/run.sh) serves the release-style build, whose API
  // base is the relative /api/v1, with `vite preview` and sets E2E_API_ORIGIN
  // to the run's backend. The preview server then forwards /api/v1 there on the
  // web app's own origin, as the reverse proxy does in production. It affects
  // `vite preview` only, never the build output or the dev server.
  preview: {
    headers: webAppHeaders,
    ...(process.env.E2E_API_ORIGIN
      ? { proxy: { '/api/v1': { target: process.env.E2E_API_ORIGIN } } }
      : {}),
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    exclude: ['e2e/**/*', 'node_modules/**/*'],
  },
});
