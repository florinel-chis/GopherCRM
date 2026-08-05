import { createBrowserRouter, Outlet } from 'react-router-dom';
import { MainLayout } from '@/layouts/MainLayout';
import { ProtectedRoute } from '@/components/ProtectedRoute';
import { Login } from '@/pages/auth/Login';
import { Register } from '@/pages/auth/Register';
import { Dashboard } from '@/pages/Dashboard';
import { NotFound } from '@/pages/NotFound';
import { Unauthorized } from '@/pages/Unauthorized';

export const router = createBrowserRouter([
  {
    path: '/login',
    element: <Login />,
  },
  {
    path: '/register',
    element: <Register />,
  },
  {
    path: '/unauthorized',
    element: <Unauthorized />,
  },
  {
    path: '/',
    element: (
      <ProtectedRoute>
        <MainLayout />
      </ProtectedRoute>
    ),
    children: [
      {
        index: true,
        element: <Dashboard />,
      },
      {
        path: 'leads',
        lazy: () => import('@/pages/leads/LeadList'),
      },
      {
        path: 'leads/new',
        lazy: () => import('@/pages/leads/LeadForm'),
      },
      {
        path: 'leads/:id',
        lazy: () => import('@/pages/leads/LeadDetail'),
      },
      {
        path: 'leads/:id/edit',
        lazy: () => import('@/pages/leads/LeadForm'),
      },
      {
        path: 'customers',
        lazy: () => import('@/pages/customers/CustomerList'),
      },
      {
        path: 'customers/new',
        lazy: () => import('@/pages/customers/CustomerForm'),
      },
      {
        path: 'customers/:id',
        lazy: () => import('@/pages/customers/CustomerDetail'),
      },
      {
        path: 'customers/:id/edit',
        lazy: () => import('@/pages/customers/CustomerForm'),
      },
      {
        path: 'tickets',
        lazy: () => import('@/pages/tickets/TicketList'),
      },
      {
        path: 'tickets/:id',
        lazy: () => import('@/pages/tickets/TicketDetail'),
      },
      {
        // Ticket writes are support/admin only on the API; keep the forms out of
        // reach of a deep link. Per-assignment precision (support may only update
        // its own tickets) is not expressible at the route level and is carried by
        // the action buttons on the detail page plus the API itself.
        element: (
          <ProtectedRoute requiredRole={['admin', 'support']}>
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'tickets/new',
            lazy: () => import('@/pages/tickets/TicketForm'),
          },
          {
            path: 'tickets/:id/edit',
            lazy: () => import('@/pages/tickets/TicketForm'),
          },
        ],
      },
      {
        path: 'tasks',
        lazy: () => import('@/pages/tasks/TaskList'),
      },
      {
        path: 'tasks/new',
        lazy: () => import('@/pages/tasks/TaskForm'),
      },
      {
        path: 'tasks/:id',
        lazy: () => import('@/pages/tasks/TaskDetail'),
      },
      {
        path: 'tasks/:id/edit',
        lazy: () => import('@/pages/tasks/TaskForm'),
      },
      {
        // Pathless layout route: applies the admin guard to every child below.
        // The guard must not live on the child routes themselves — a statically
        // defined `element` takes precedence over `lazy`, which would silently
        // discard the real page component.
        element: (
          <ProtectedRoute requiredRole="admin">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'users',
            lazy: () => import('@/pages/users/UserList'),
          },
          {
            path: 'users/new',
            lazy: () => import('@/pages/users/UserForm'),
          },
          {
            path: 'users/:id',
            lazy: () => import('@/pages/users/UserDetail'),
          },
          {
            path: 'users/:id/edit',
            lazy: () => import('@/pages/users/UserForm'),
          },
          {
            path: 'settings/configuration',
            lazy: () => import('@/pages/settings/ConfigurationSettings'),
          },
        ],
      },
      {
        path: 'settings/profile',
        lazy: () => import('@/pages/settings/Profile'),
      },
      {
        path: 'settings/api-keys',
        lazy: () => import('@/pages/settings/APIKeys'),
      },
    ],
  },
  {
    path: '*',
    element: <NotFound />,
  },
]);