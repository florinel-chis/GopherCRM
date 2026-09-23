import { describe, it, expect, vi } from 'vitest';
import { renderHook, act, screen } from '@testing-library/react';
import { useSnackbar } from './useSnackbar';
import { SnackbarProvider } from '@/contexts/SnackbarContext';
import React from 'react';

const wrapper = ({ children }: { children: React.ReactNode }) => (
  <SnackbarProvider>{children}</SnackbarProvider>
);

describe('useSnackbar', () => {
  it('provides showSuccess function', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });
    
    expect(result.current.showSuccess).toBeDefined();
    expect(typeof result.current.showSuccess).toBe('function');
  });

  it('provides showError function', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });
    
    expect(result.current.showError).toBeDefined();
    expect(typeof result.current.showError).toBe('function');
  });

  it('provides showWarning function', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });
    
    expect(result.current.showWarning).toBeDefined();
    expect(typeof result.current.showWarning).toBe('function');
  });

  it('provides showInfo function', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });
    
    expect(result.current.showInfo).toBeDefined();
    expect(typeof result.current.showInfo).toBe('function');
  });

  it('shows the message as a success alert', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });

    act(() => {
      result.current.showSuccess('Lead saved');
    });

    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Lead saved');
    expect(alert.className).toMatch(/MuiAlert-colorSuccess/);
  });

  it('shows the message as an error alert', () => {
    const { result } = renderHook(() => useSnackbar(), { wrapper });

    act(() => {
      result.current.showError('Save failed');
    });

    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Save failed');
    expect(alert.className).toMatch(/MuiAlert-colorError/);
  });

  it('throws error when used outside provider', () => {
    // Mock console.error to prevent test output pollution
    const consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    
    expect(() => {
      renderHook(() => useSnackbar());
    }).toThrow('useSnackbar must be used within a SnackbarProvider');
    
    consoleSpy.mockRestore();
  });
});