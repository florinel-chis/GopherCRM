import { createBrowserRouter } from 'react-router-dom';
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
        path: 'tickets/new',
        lazy: () => import('@/pages/tickets/TicketForm'),
      },
      {
        path: 'tickets/:id',
        lazy: () => import('@/pages/tickets/TicketDetail'),
      },
      {
        path: 'tickets/:id/edit',
        lazy: () => import('@/pages/tickets/TicketForm'),
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
        path: 'users',
        element: (
          <ProtectedRoute requiredRole="admin">
            <div>Users List (Admin only)</div>
          </ProtectedRoute>
        ),
        lazy: () => import('@/pages/users/UserList'),
      },
      {
        path: 'users/new',
        element: (
          <ProtectedRoute requiredRole="admin">
            <div>New User (Admin only)</div>
          </ProtectedRoute>
        ),
        lazy: () => import('@/pages/users/UserForm'),
      },
      {
        path: 'users/:id',
        element: (
          <ProtectedRoute requiredRole="admin">
            <div>User Detail (Admin only)</div>
          </ProtectedRoute>
        ),
        lazy: () => import('@/pages/users/UserDetail'),
      },
      {
        path: 'users/:id/edit',
        element: (
          <ProtectedRoute requiredRole="admin">
            <div>Edit User (Admin only)</div>
          </ProtectedRoute>
        ),
        lazy: () => import('@/pages/users/UserForm'),
      },
      {
        path: 'settings/profile',
        lazy: () => import('@/pages/settings/Profile'),
      },
      {
        path: 'settings/api-keys',
        lazy: () => import('@/pages/settings/APIKeys'),
      },
      {
        path: 'settings/configuration',
        element: (
          <ProtectedRoute requiredRole="admin">
            <div>Configuration Settings (Admin only)</div>
          </ProtectedRoute>
        ),
        lazy: () => import('@/pages/settings/ConfigurationSettings'),
      },
    ],
  },
  {
    path: '*',
    element: <NotFound />,
  },
]);