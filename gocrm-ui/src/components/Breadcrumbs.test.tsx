import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Breadcrumbs } from './Breadcrumbs';
import { MemoryRouter } from 'react-router-dom';

const renderWithRouter = (initialEntries: string[]) => {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <Breadcrumbs />
    </MemoryRouter>
  );
};

describe('Breadcrumbs', () => {
  it('renders nothing on dashboard (root) page', () => {
    const { container } = renderWithRouter(['/']);
    expect(container.firstChild).toBeNull();
  });

  it('renders breadcrumbs for leads page', () => {
    renderWithRouter(['/leads']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Leads')).toBeInTheDocument();
    
    // Dashboard should be a link
    const dashboardLink = screen.getByRole('link', { name: 'Dashboard' });
    expect(dashboardLink).toHaveAttribute('href', '/');
    
    // Current page should not be a link
    expect(screen.getByText('Leads').closest('a')).toBeNull();
  });

  it('renders breadcrumbs for nested pages', () => {
    renderWithRouter(['/leads/new']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Leads')).toBeInTheDocument();
    expect(screen.getByText('New')).toBeInTheDocument();
    
    // Dashboard and Leads should be links
    expect(screen.getByRole('link', { name: 'Dashboard' })).toHaveAttribute('href', '/');
    expect(screen.getByRole('link', { name: 'Leads' })).toHaveAttribute('href', '/leads');
    
    // New should not be a link
    expect(screen.getByText('New').closest('a')).toBeNull();
  });

  it('skips numeric IDs in breadcrumbs', () => {
    renderWithRouter(['/leads/123/edit']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Leads')).toBeInTheDocument();
    expect(screen.getByText('Edit')).toBeInTheDocument();
    
    // Should not show the ID
    expect(screen.queryByText('123')).not.toBeInTheDocument();
  });

  it('handles customer pages', () => {
    renderWithRouter(['/customers']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Customers')).toBeInTheDocument();
  });

  it('handles ticket pages with proper labels', () => {
    renderWithRouter(['/tickets/new']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Tickets')).toBeInTheDocument();
    expect(screen.getByText('New')).toBeInTheDocument();
  });

  it('handles user profile pages', () => {
    renderWithRouter(['/users/profile']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Users')).toBeInTheDocument();
    expect(screen.getByText('Profile')).toBeInTheDocument();
  });

  it('capitalizes unknown segments', () => {
    renderWithRouter(['/unknown-page']);

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Unknown-page')).toBeInTheDocument();
  });

  it('uses separator icon between breadcrumbs', () => {
    renderWithRouter(['/leads/new']);

    // Check for separator icons (NavigateNext)
    const separators = screen.getAllByTestId('NavigateNextIcon');
    expect(separators).toHaveLength(2); // Between Dashboard-Leads and Leads-New
  });
});