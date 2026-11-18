import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook } from '@testing-library/react';
import { usePermissions } from './usePermissions';
import { createMockUser } from '@/test/factories';
import type { Permission, User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const signedIn = (user: User, permissions: Permission[] | undefined) =>
  mockUseAuth.mockReturnValue({
    user,
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

describe('usePermissions', () => {
  beforeEach(() => {
    mockUseAuth.mockReset();
  });

  it('grants nothing when the API listed no permissions', () => {
    const admin = createMockUser({ id: 1, role: 'admin' });

    for (const permissions of [[], undefined]) {
      signedIn(admin, permissions);
      const { result } = renderHook(() => usePermissions());

      expect(result.current.can('users.view')).toBe(false);
      expect(result.current.can(['users.view', 'leads.view'])).toBe(false);
      expect(result.current.can([])).toBe(false);
      expect(result.current.scopeOf('users.view')).toBeNull();
      expect(result.current.canOnRecord('deals.update', admin.id)).toBe(false);
    }
  });

  it('grants a listed key, any key of a list, and reports the scope', () => {
    // The role name is deliberately the weakest one: only the list decides.
    signedIn(createMockUser({ id: 7, role: 'customer' }), [
      { key: 'leads.view', scope: 'own' },
      { key: 'tickets.update', scope: 'all' },
    ]);
    const { result } = renderHook(() => usePermissions());

    expect(result.current.can('leads.view')).toBe(true);
    expect(result.current.can('leads.create')).toBe(false);
    expect(result.current.can(['leads.create', 'tickets.update'])).toBe(true);
    expect(result.current.can(['leads.create', 'deals.view'])).toBe(false);
    expect(result.current.scopeOf('leads.view')).toBe('own');
    expect(result.current.scopeOf('tickets.update')).toBe('all');
    expect(result.current.scopeOf('deals.view')).toBeNull();
  });

  it('applies the per-record rule: all covers every record, own only the caller’s', () => {
    signedIn(createMockUser({ id: 7, role: 'sales' }), [
      { key: 'deals.update', scope: 'own' },
      { key: 'tickets.update', scope: 'all' },
    ]);
    const { result } = renderHook(() => usePermissions());

    expect(result.current.canOnRecord('deals.update', 7)).toBe(true);
    expect(result.current.canOnRecord('deals.update', 8)).toBe(false);
    expect(result.current.canOnRecord('deals.update', undefined)).toBe(false);
    expect(result.current.canOnRecord('tickets.update', 8)).toBe(true);
    expect(result.current.canOnRecord('tickets.update', undefined)).toBe(true);
    expect(result.current.canOnRecord('deals.delete', 7)).toBe(false);
  });
});
