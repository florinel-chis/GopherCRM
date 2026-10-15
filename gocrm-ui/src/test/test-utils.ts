import type React from 'react';
import { render } from '@testing-library/react';
import type { RenderOptions } from '@testing-library/react';
import { TestProviders } from './TestProviders';

const customRender = (
  ui: React.ReactElement,
  options?: Omit<RenderOptions, 'wrapper'>
) => render(ui, { wrapper: TestProviders, ...options });

export * from '@testing-library/react';
export { customRender as render };
export { requireElement } from './assertions';
