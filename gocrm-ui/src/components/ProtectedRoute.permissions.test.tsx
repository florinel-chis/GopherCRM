import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import { ProtectedRoute } from './ProtectedRoute';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

// The role is admin throughout: the guard must follow the list alone.
const signedInWith = (permissions: Permission[]) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: 1, role: 'admin' }),
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

const renderGuarded = (requiredPermission: string | string[]) =>
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
    </MemoryRouter>,
  );

describe('ProtectedRoute permission guard', () => {
  beforeEach(() => {
    mockUseAuth.mockReset();
  });

  it('redirects to /unauthorized when the permission is missing', () => {
    signedInWith(customPermissions('leads.view'));
    const single = renderGuarded('tickets.view');
    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
    single.unmount();

    renderGuarded(['tickets.view', 'tickets.view_own_customer']);
    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByText('Protected Content')).not.toBeInTheDocument();
  });

  it('redirects an admin-named user whose permission list is empty', () => {
    signedInWith([]);
    renderGuarded('users.view');
    expect(screen.getByText('Unauthorized Page')).toBeInTheDocument();
  });

  it('renders the page for a caller holding the single key', () => {
    signedInWith(customPermissions(['tickets.view', 'own']));
    renderGuarded('tickets.view');
    expect(screen.getByText('Protected Content')).toBeInTheDocument();
    expect(screen.queryByText('Unauthorized Page')).not.toBeInTheDocument();
  });

  it('renders the page for a caller holding one key of an any-of list', () => {
    signedInWith(customPermissions('tickets.view_own_customer'));
    renderGuarded(['tickets.view', 'tickets.view_own_customer']);
    expect(screen.getByText('Protected Content')).toBeInTheDocument();
    expect(screen.queryByText('Unauthorized Page')).not.toBeInTheDocument();
  });
});
