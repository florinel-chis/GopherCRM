/// <reference types="node" />
/**
 * Where the app under test lives, for specs that call the API directly or need
 * to recognise the UI origin.
 *
 * `make e2e` (scripts/e2e/run.sh) exports VITE_API_BASE_URL and E2E_UI_PORT for
 * the backend and Vite server it starts. Manual runs fall back to the local dev
 * defaults: gocrm-ui/.env points the UI at :8090, and Vite serves on :5173.
 */
export const API_BASE_URL = process.env.VITE_API_BASE_URL ?? 'http://localhost:8090/api/v1';

export const UI_BASE_URL = `http://localhost:${process.env.E2E_UI_PORT ?? '5173'}`;
