import { createBrowserRouter, Navigate, Outlet } from 'react-router-dom';
import { MainLayout } from '@/layouts/MainLayout';
import { ProtectedRoute } from '@/components/ProtectedRoute';
import { RouteErrorBoundary } from '@/components/ErrorBoundary';
import { Login } from '@/pages/auth/Login';
import { Register } from '@/pages/auth/Register';
import { ForgotPassword } from '@/pages/auth/ForgotPassword';
import { ResetPassword } from '@/pages/auth/ResetPassword';
import { Dashboard } from '@/pages/Dashboard';
import { NotFound } from '@/pages/NotFound';
import { Unauthorized } from '@/pages/Unauthorized';

export const router = createBrowserRouter([
  {
    path: '/login',
    element: <Login />,
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '/register',
    element: <Register />,
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '/forgot-password',
    element: <ForgotPassword />,
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '/reset-password',
    element: <ResetPassword />,
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '/unauthorized',
    element: <Unauthorized />,
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '/',
    element: (
      <ProtectedRoute>
        <MainLayout />
      </ProtectedRoute>
    ),
    // A data router catches render errors in its own routes before they can
    // reach the boundary wrapped around <RouterProvider>, so every top-level
    // route carries one. Without it a crashing page falls through to React
    // Router's built-in error screen.
    errorElement: <RouteErrorBoundary />,
    children: [
      {
        index: true,
        element: <Dashboard />,
      },
      {
        // Every guard in this table names the permission key the API asks for the
        // page's main call (internal/permissions/routes.go). The keys come from
        // GET /users/me, never from the role name; a caller without the key lands
        // on /unauthorized instead of a page of refused requests.
        //
        // The guards are pathless layout routes: a static `element` on a child
        // itself would take precedence over `lazy` and silently discard the real
        // page component.
        element: (
          <ProtectedRoute requiredPermission="leads.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'leads',
            lazy: () => import('@/pages/leads/LeadList'),
          },
          {
            path: 'leads/:id',
            lazy: () => import('@/pages/leads/LeadDetail'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="leads.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above leads/:id.
            path: 'leads/new',
            lazy: () => import('@/pages/leads/LeadForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="leads.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'leads/:id/edit',
            lazy: () => import('@/pages/leads/LeadForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="customers.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above customers/:id.
            path: 'customers/new',
            lazy: () => import('@/pages/customers/CustomerForm'),
          },
        ],
      },
      {
        // The customer pages need the read key: a caller without it (the customer
        // system role) is redirected instead of shown a list the API refuses.
        element: (
          <ProtectedRoute requiredPermission="customers.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'customers',
            lazy: () => import('@/pages/customers/CustomerList'),
          },
          {
            path: 'customers/:id',
            lazy: () => import('@/pages/customers/CustomerDetail'),
          },
        ],
      },
      {
        // The edit form saves through PUT /customers/:id.
        element: (
          <ProtectedRoute requiredPermission="customers.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'customers/:id/edit',
            lazy: () => import('@/pages/customers/CustomerForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="companies.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'companies',
            lazy: () => import('@/pages/companies/CompanyList'),
          },
          {
            path: 'companies/:id',
            lazy: () => import('@/pages/companies/CompanyDetail'),
          },
        ],
      },
      {
        // A read-only caller must not reach the form through a deep link.
        element: (
          <ProtectedRoute requiredPermission="companies.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above companies/:id.
            path: 'companies/new',
            lazy: () => import('@/pages/companies/CompanyForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="companies.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'companies/:id/edit',
            lazy: () => import('@/pages/companies/CompanyForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="deals.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'deals',
            lazy: () => import('@/pages/deals/DealList'),
          },
          {
            // Static segment: ranks above deals/:id.
            path: 'deals/board',
            lazy: () => import('@/pages/deals/DealBoard'),
          },
          {
            path: 'deals/:id',
            lazy: () => import('@/pages/deals/DealDetail'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="deals.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above deals/:id.
            path: 'deals/new',
            lazy: () => import('@/pages/deals/DealForm'),
          },
        ],
      },
      {
        // Whether this caller may edit this deal (scope own) is decided by the
        // buttons on the detail page and by the API.
        element: (
          <ProtectedRoute requiredPermission="deals.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'deals/:id/edit',
            lazy: () => import('@/pages/deals/DealForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="products.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'products',
            lazy: () => import('@/pages/products/ProductList'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="products.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'products/new',
            lazy: () => import('@/pages/products/ProductForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="products.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'products/:id/edit',
            lazy: () => import('@/pages/products/ProductForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="quotes.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'quotes',
            lazy: () => import('@/pages/quotes/QuoteList'),
          },
          {
            path: 'quotes/:id',
            lazy: () => import('@/pages/quotes/QuoteDetail'),
          },
          {
            // A print-styled route inside the shell: the layout hides its
            // chrome under @media print.
            path: 'quotes/:id/print',
            lazy: () => import('@/pages/quotes/QuotePrint'),
          },
        ],
      },
      {
        // The run history; the API narrows a caller at scope own to the runs of
        // its own leads and deals. Cancelling is gated on the page.
        element: (
          <ProtectedRoute requiredPermission="automation.runs_view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // /automation itself has no page; the breadcrumb links here.
            path: 'automation',
            element: <Navigate to="/automation/runs" replace />,
          },
          {
            path: 'automation/runs',
            lazy: () => import('@/pages/automation/AutomationRunList'),
          },
          {
            path: 'automation/runs/:id',
            lazy: () => import('@/pages/automation/AutomationRunDetail'),
          },
        ],
      },
      {
        // Every /automation/rules call (and the dry run) asks for this key.
        element: (
          <ProtectedRoute requiredPermission="automation.rules_manage">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'automation/rules',
            lazy: () => import('@/pages/automation/AutomationRuleList'),
          },
          {
            path: 'automation/rules/new',
            lazy: () => import('@/pages/automation/AutomationRuleForm'),
          },
          {
            path: 'automation/rules/:id/edit',
            lazy: () => import('@/pages/automation/AutomationRuleForm'),
          },
        ],
      },
      {
        // The caller's own notifications.
        element: (
          <ProtectedRoute requiredPermission="notifications.use">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'notifications',
            lazy: () => import('@/pages/notifications/NotificationList'),
          },
        ],
      },
      {
        // The ticket list and detail open with either read key:
        // tickets.view_own_customer is the read of a customer login, narrowed by
        // the API to the tickets of its linked customer record.
        element: (
          <ProtectedRoute requiredPermission={['tickets.view', 'tickets.view_own_customer']}>
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'tickets',
            lazy: () => import('@/pages/tickets/TicketList'),
          },
          {
            path: 'tickets/:id',
            lazy: () => import('@/pages/tickets/TicketDetail'),
          },
        ],
      },
      {
        // Keeps the ticket forms out of reach of a deep link.
        element: (
          <ProtectedRoute requiredPermission="tickets.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above tickets/:id.
            path: 'tickets/new',
            lazy: () => import('@/pages/tickets/TicketForm'),
          },
        ],
      },
      {
        // Per-assignment precision (scope own: only the caller's own tickets) is
        // not expressible at the route level; the action buttons on the detail
        // page and the API carry it.
        element: (
          <ProtectedRoute requiredPermission="tickets.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'tickets/:id/edit',
            lazy: () => import('@/pages/tickets/TicketForm'),
          },
        ],
      },
      {
        // Tasks: the read key opens every task page; the API decides which
        // tasks the caller sees and may change.
        element: (
          <ProtectedRoute requiredPermission="tasks.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'tasks',
            lazy: () => import('@/pages/tasks/TaskList'),
          },
          {
            // Static segment: ranks above tasks/:id.
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
        ],
      },
      {
        // The create, edit and delete affordances are gated inside the page.
        element: (
          <ProtectedRoute requiredPermission="labels.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'labels',
            lazy: () => import('@/pages/labels/LabelList'),
          },
        ],
      },
      {
        // AEO is internal marketing data: the whole group needs aeo.view.
        element: (
          <ProtectedRoute requiredPermission="aeo.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'aeo',
            lazy: () => import('@/pages/aeo/AEODashboard'),
          },
          {
            path: 'aeo/prompts',
            lazy: () => import('@/pages/aeo/AEOPrompts'),
          },
          {
            path: 'aeo/citations',
            lazy: () => import('@/pages/aeo/AEOCitations'),
          },
          {
            path: 'aeo/settings',
            lazy: () => import('@/pages/aeo/AEOSettings'),
          },
        ],
      },
      {
        // Forms open with the read key. A caller without forms.manage is
        // read-only: the builder renders without its save affordances, and the
        // API refuses the write anyway.
        element: (
          <ProtectedRoute requiredPermission="forms.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'forms',
            lazy: () => import('@/pages/forms/FormList'),
          },
          {
            path: 'forms/new',
            lazy: () => import('@/pages/forms/FormBuilder'),
          },
          {
            path: 'forms/:id',
            lazy: () => import('@/pages/forms/FormDetail'),
          },
          {
            path: 'forms/:id/edit',
            lazy: () => import('@/pages/forms/FormBuilder'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="users.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'users',
            lazy: () => import('@/pages/users/UserList'),
          },
          {
            path: 'users/:id',
            lazy: () => import('@/pages/users/UserDetail'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="users.create">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above users/:id.
            path: 'users/new',
            lazy: () => import('@/pages/users/UserForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="users.update">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'users/:id/edit',
            lazy: () => import('@/pages/users/UserForm'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="settings.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'settings/configuration',
            lazy: () => import('@/pages/settings/ConfigurationSettings'),
          },
        ],
      },
      {
        element: (
          <ProtectedRoute requiredPermission="roles.manage">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            // Static segment: ranks above settings/roles/:id.
            path: 'settings/roles/new',
            lazy: () => import('@/pages/settings/RoleEditor'),
          },
        ],
      },
      {
        // The editor shows a role read-only to a caller without roles.manage.
        element: (
          <ProtectedRoute requiredPermission="roles.view">
            <Outlet />
          </ProtectedRoute>
        ),
        children: [
          {
            path: 'settings/roles',
            lazy: () => import('@/pages/settings/RoleList'),
          },
          {
            path: 'settings/roles/:id',
            lazy: () => import('@/pages/settings/RoleEditor'),
          },
        ],
      },
      // The dashboard above, the profile and the caller's own API keys need no
      // key: every signed-in user reaches them.
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
  // The public page behind the link of a quote email (#383): reached without
  // a session, outside the guard and the application shell.
  {
    path: '/quote-response',
    lazy: () => import('@/pages/quotes/QuoteResponse'),
    errorElement: <RouteErrorBoundary />,
  },
  // The page the server's headless browser prints to PDF (#384): public for
  // the same reason, and readable only with the credential of a running
  // render in the URL fragment.
  {
    path: '/quote-render',
    lazy: () => import('@/pages/quotes/QuoteRender'),
    errorElement: <RouteErrorBoundary />,
  },
  {
    path: '*',
    element: <NotFound />,
    errorElement: <RouteErrorBoundary />,
  },
]);
