import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { ProtectedRoute } from './ProtectedRoute';
import { createMockUser } from '@/test/factories';
import type { Permission, User } from '@/types';
import { permissionsOf } from '@/test/permissions';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (overrides?: {
  user?: User | null;
  permissions?: Permission[];
  isLoading?: boolean;
  isAuthenticated?: boolean;
}) => ({
  user: null,
  permissions: permissionsOf(null),
  isLoading: false,
  isAuthenticated: false,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
  ...overrides,
});

const renderProtected = (requiredPermission?: string | string[]) =>
  render(
    <MemoryRouter initialEntries={['/secret']}>
      <Routes>
        <Route path="/login" element={<div>Login Page</div>} />
        <Route path="/unauthorized" element={<div>Unauthorized Page</div>} />
        <Route
          path="/secret"
          element={
            <ProtectedRoute requiredPermission={requiredPermission}>
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

  it('renders children for an authenticated user when no permission is required', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'customer' }),
        permissions: permissionsOf(createMockUser({ role: 'customer' })),
        isAuthenticated: true,
      })
    );

    renderProtected();

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
    expect(screen.queryByText('Login Page')).not.toBeInTheDocument();
    expect(screen.queryByText('Unauthorized Page')).not.toBeInTheDocument();
  });

  it('renders children when the user holds the required permission', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'admin' }),
        permissions: permissionsOf(createMockUser({ role: 'admin' })),
        isAuthenticated: true,
      })
    );

    renderProtected('users.view');

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
  });

  it('redirects authenticated users without the required permission to /unauthorized', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'sales' }),
        permissions: permissionsOf(createMockUser({ role: 'sales' })),
        isAuthenticated: true,
      })
    );

    renderProtected('users.view');

    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });

  it('accepts a list of permissions and renders children when the user holds one of them', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'sales' }),
        permissions: permissionsOf(createMockUser({ role: 'sales' })),
        isAuthenticated: true,
      })
    );

    renderProtected(['users.view', 'leads.view']);

    expect(screen.getByText('Protected Content')).toBeInTheDocument();
  });

  it('redirects to /unauthorized when the user holds none of the listed permissions', () => {
    mockUseAuth.mockReturnValue(
      authState({
        user: createMockUser({ role: 'customer' }),
        permissions: permissionsOf(createMockUser({ role: 'customer' })),
        isAuthenticated: true,
      })
    );

    renderProtected(['users.view', 'leads.view']);

    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });
});
