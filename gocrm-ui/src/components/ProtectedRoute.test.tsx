import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { ProtectedRoute } from './ProtectedRoute';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (overrides?: {
  user?: User | null;
  isLoading?: boolean;
  isAuthenticated?: boolean;
}) => ({
  user: null,
  isLoading: false,
  isAuthenticated: false,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
  ...overrides,
});

const renderProtected = (requiredRole?: string | string[]) =>
  render(
    <MemoryRouter initialEntries={['/secret']}>
      <Routes>
        <Route path="/login" element={<div>Login Page</div>} />
        <Route path="/unauthorized" element={<div>Unauthorized Page</div>} />
        <Route
          path="/secret"
          element={
            <ProtectedRoute requiredRole={requiredRole}>
              <div>Protected Content</div>
            </ProtectedRoute>
          }
        />
      </Routes>
    </MemoryRouter>
  );

describe('ProtectedRoute', () => {
  beforeEach(() => {
    mockUseAuth.mockReset();
  });

  it('shows a loading spinner while auth state is loading (no redirect, no content)', () => {
    mockUseAuth.mockReturnValue(authState({ isLoading: true }));

    renderProtected();

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
    expect(screen.queryByText('Login Page')).not.toBeInTheDocument();
    expect(screen.queryByText('Unauthorized Page')).not.toBeInTheDocument();
  });

  it('redirects unauthenticated users to /login', () => {
    mockUseAuth.mockReturnValue(authState());

    renderProtected();

    expect(screen.getByText('Login Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });

  it('renders children for an authenticated user when no role is required', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'customer' }),
        isAuthenticated: true,
      })
    );

    renderProtected();

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
    expect(screen.queryByText('Login Page')).not.toBeInTheDocument();
    expect(screen.queryByText('Unauthorized Page')).not.toBeInTheDocument();
  });

  it('renders children when the user has the required role', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'admin' }),
        isAuthenticated: true,
      })
    );

    renderProtected('admin');

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
  });

  it('redirects authenticated users without the required role to /unauthorized', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'sales' }),
        isAuthenticated: true,
      })
    );

    renderProtected('admin');

    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });

  it('accepts an array of roles and renders children when the user matches one of them', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'sales' }),
        isAuthenticated: true,
      })
    );

    renderProtected(['admin', 'sales']);

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
  });

  it('redirects to /unauthorized when the user role is not in the required role array', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'customer' }),
        isAuthenticated: true,
      })
    );

    renderProtected(['admin', 'sales']);

    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });
});
