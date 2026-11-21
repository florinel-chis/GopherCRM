import React, { useState } from 'react';
import { Outlet, useNavigate } from 'react-router-dom';
import { Breadcrumbs } from '@/components/Breadcrumbs';
import {
  AppBar,
  Box,
  CssBaseline,
  Drawer,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Toolbar,
  Typography,
  Divider,
  Avatar,
  Menu,
  MenuItem,
  Collapse,
} from '@mui/material';
import {
  Menu as MenuIcon,
  Dashboard,
  People,
  Business,
  Apartment,
  Assignment,
  Task,
  ExpandLess,
  ExpandMore,
  Settings,
  Key,
  Logout,
  AccountCircle,
  ContactPhone,
  Handshake,
  Inventory2,
  RequestQuote,
  ChevronLeft,
  Tune,
  LocalOffer,
  DynamicForm,
  Insights,
  Timeline,
  QuestionAnswer,
  Link as LinkIcon,
  AutoMode,
  Rule,
  History,
  ManageHistory,
  VpnKey,
  Sync,
} from '@mui/icons-material';
import { NotificationBell } from '@/components/layout/NotificationBell';
import { useAuth } from '@/hooks/useAuth';
import { usePermissions } from '@/hooks/usePermissions';
import { useProductName } from '@/hooks/useProductName';

const drawerWidth = 280;

interface NavEntry {
  title: string;
  icon: React.ReactNode;
  // The permission key the entry needs, or a list of which any one is
  // enough. An entry without one is shown to every signed-in user.
  permission?: string | string[];
}

type NavLink = NavEntry & { path: string; children?: undefined };
type NavItem = NavLink | (NavEntry & { children: NavLink[]; path?: undefined });

const navItems: NavItem[] = [
  {
    title: 'Dashboard',
    path: '/',
    icon: <Dashboard />,
  },
  {
    title: 'Leads',
    path: '/leads',
    icon: <ContactPhone />,
    permission: 'leads.view',
  },
  {
    // Shown to a caller that may read deals.
    title: 'Deals',
    path: '/deals',
    icon: <Handshake />,
    permission: 'deals.view',
  },
  {
    // Shown to a caller that may read the catalogue; the page itself narrows
    // the writes.
    title: 'Products',
    path: '/products',
    icon: <Inventory2 />,
    permission: 'products.view',
  },
  {
    // Shown to a caller that may read quotes.
    title: 'Quotes',
    path: '/quotes',
    icon: <RequestQuote />,
    permission: 'quotes.view',
  },
  {
    // The group shows when either child does: the rules need
    // automation.rules_manage, the run history automation.runs_view (the API
    // narrows a caller at scope own to its own leads and deals).
    title: 'Automation',
    icon: <AutoMode />,
    permission: ['automation.rules_manage', 'automation.runs_view'],
    children: [
      {
        title: 'Rules',
        path: '/automation/rules',
        icon: <Rule />,
        permission: 'automation.rules_manage',
      },
      {
        title: 'Runs',
        path: '/automation/runs',
        icon: <History />,
        permission: 'automation.runs_view',
      },
    ],
  },
  {
    title: 'Customers',
    path: '/customers',
    icon: <Business />,
    permission: 'customers.view',
  },
  {
    // Shown to a caller that may read companies.
    title: 'Companies',
    path: '/companies',
    icon: <Apartment />,
    permission: 'companies.view',
  },
  {
    // Shown with tickets.view. A caller holding only tickets.view_own_customer
    // gets no entry, although the route admits it. The ticket pages narrow
    // create, edit and delete by their own keys.
    title: 'Tickets',
    path: '/tickets',
    icon: <Assignment />,
    permission: 'tickets.view',
  },
  {
    title: 'Tasks',
    path: '/tasks',
    icon: <Task />,
    permission: 'tasks.view',
  },
  {
    title: 'Labels',
    path: '/labels',
    icon: <LocalOffer />,
    permission: 'labels.view',
  },
  {
    // Web forms served to external sites. A caller without forms.manage sees
    // the pages read-only.
    title: 'Forms',
    path: '/forms',
    icon: <DynamicForm />,
    permission: 'forms.view',
  },
  {
    // Answer Engine Optimization. A caller without aeo.view never sees the
    // entry and is bounced by the route guard.
    title: 'AEO',
    icon: <Insights />,
    permission: 'aeo.view',
    children: [
      {
        title: 'Dashboard',
        path: '/aeo',
        icon: <Timeline />,
      },
      {
        title: 'Prompts',
        path: '/aeo/prompts',
        icon: <QuestionAnswer />,
      },
      {
        title: 'Citations',
        path: '/aeo/citations',
        icon: <LinkIcon />,
      },
      {
        title: 'Settings',
        path: '/aeo/settings',
        icon: <Tune />,
      },
    ],
  },
  {
    title: 'Users',
    path: '/users',
    icon: <People />,
    permission: 'users.view',
  },
  {
    title: 'Settings',
    icon: <Settings />,
    children: [
      {
        title: 'Profile',
        path: '/settings/profile',
        icon: <AccountCircle />,
      },
      {
        title: 'API Keys',
        path: '/settings/api-keys',
        icon: <Key />,
      },
      {
        title: 'Configuration',
        path: '/settings/configuration',
        icon: <Tune />,
        permission: 'settings.view',
      },
      {
        // The role list; the screens narrow the writes by roles.manage.
        title: 'Roles',
        path: '/settings/roles',
        icon: <People />,
        permission: 'roles.view',
      },
      {
        // Outbound webhook endpoints and the delivery log.
        title: 'Webhooks',
        path: '/settings/webhooks',
        icon: <LinkIcon />,
        permission: 'webhooks.manage',
      },
      {
        // OpenID Connect issuer, client and role mapping.
        title: 'Single sign-on',
        path: '/settings/sso',
        icon: <VpnKey />,
        permission: 'settings.sso_manage',
      },
      {
        // Password, single sign-on, refresh and API-key sign-in outcomes.
        title: 'Sign-in audit',
        path: '/settings/sign-in-audit',
        icon: <History />,
        permission: 'audit.sign_ins_view',
      },
      {
        // Role changes and account activation, deactivation, erasure and
        // email changes (#552).
        title: 'Role audit',
        path: '/settings/role-audit',
        icon: <ManageHistory />,
        permission: 'roles.audit_view',
      },
      {
        // Bearer tokens for an identity provider's SCIM client (#490).
        title: 'SCIM tokens',
        path: '/settings/scim-tokens',
        icon: <Sync />,
        permission: 'settings.scim_manage',
      },
    ],
  },
];

export const MainLayout: React.FC = () => {
  const [mobileOpen, setMobileOpen] = useState(false);
  // Keyed by nav title: there is more than one collapsible group now (AEO and
  // Settings), and a single shared boolean would open and close them together.
  const [openGroups, setOpenGroups] = useState<Record<string, boolean>>({});
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const navigate = useNavigate();
  const { user, logout } = useAuth();
  const { can } = usePermissions();
  const productName = useProductName();

  const handleDrawerToggle = () => {
    setMobileOpen(!mobileOpen);
  };

  const handleGroupToggle = (title: string) => {
    setOpenGroups((previous) => ({ ...previous, [title]: !previous[title] }));
  };

  const handleMenuOpen = (event: React.MouseEvent<HTMLElement>) => {
    setAnchorEl(event.currentTarget);
  };

  const handleMenuClose = () => {
    setAnchorEl(null);
  };

  const handleLogout = async () => {
    await logout();
    navigate('/login');
  };

  const canViewNavItem = (item: NavItem): boolean => {
    if (item.permission && !can(item.permission)) return false;
    // A group with no visible child is not shown.
    if (item.children) return item.children.some((child) => canViewNavItem(child));
    return true;
  };

  const drawer = (
    <Box>
      <Toolbar>
        <Typography variant="h6" noWrap component="div">
          {productName}
        </Typography>
        <IconButton
          sx={{ ml: 'auto', display: { sm: 'none' } }}
          onClick={handleDrawerToggle}
        >
          <ChevronLeft />
        </IconButton>
      </Toolbar>
      <Divider />
      <List>
        {navItems.map((item) => {
          if (!canViewNavItem(item)) return null;

          if (item.children) {
            const groupOpen = Boolean(openGroups[item.title]);
            return (
              <React.Fragment key={item.title}>
                <ListItemButton onClick={() => handleGroupToggle(item.title)}>
                  <ListItemIcon>{item.icon}</ListItemIcon>
                  <ListItemText primary={item.title} />
                  {groupOpen ? <ExpandLess /> : <ExpandMore />}
                </ListItemButton>
                <Collapse in={groupOpen} timeout="auto" unmountOnExit>
                  <List component="div" disablePadding>
                    {item.children.map((child) => {
                      if (!canViewNavItem(child)) return null;
                      return (
                        <ListItemButton
                          key={child.title}
                          sx={{ pl: 4 }}
                          onClick={() => {
                            navigate(child.path);
                            setMobileOpen(false);
                          }}
                        >
                          <ListItemIcon>{child.icon}</ListItemIcon>
                          <ListItemText primary={child.title} />
                        </ListItemButton>
                      );
                    })}
                  </List>
                </Collapse>
              </React.Fragment>
            );
          }

          return (
            <ListItem key={item.title} disablePadding>
              <ListItemButton
                onClick={() => {
                  navigate(item.path);
                  setMobileOpen(false);
                }}
              >
                <ListItemIcon>{item.icon}</ListItemIcon>
                <ListItemText primary={item.title} />
              </ListItemButton>
            </ListItem>
          );
        })}
      </List>
    </Box>
  );

  // The application chrome (app bar, navigation drawers, breadcrumbs) is
  // hidden under `@media print` so that a print-styled page such as
  // /quotes/:id/print prints its content alone. `displayPrint` is the MUI
  // system shorthand for exactly that media query.
  return (
    <Box sx={{ display: 'flex' }}>
      <CssBaseline />
      <AppBar
        position="fixed"
        data-testid="app-bar"
        sx={{
          width: { sm: `calc(100% - ${drawerWidth}px)` },
          ml: { sm: `${drawerWidth}px` },
          displayPrint: 'none',
        }}
      >
        <Toolbar>
          <IconButton
            color="inherit"
            aria-label="open drawer"
            edge="start"
            onClick={handleDrawerToggle}
            sx={{ mr: 2, display: { sm: 'none' } }}
          >
            <MenuIcon />
          </IconButton>
          <Typography variant="h6" noWrap component="div" sx={{ flexGrow: 1 }}>
            {/* Dynamic page title can be added here */}
          </Typography>

          <NotificationBell />

          <IconButton
            onClick={handleMenuOpen}
            color="inherit"
            sx={{ ml: 2 }}
          >
            <Avatar sx={{ width: 32, height: 32 }}>
              {user?.first_name?.[0]?.toUpperCase() || 'U'}
            </Avatar>
          </IconButton>
          <Menu
            anchorEl={anchorEl}
            open={Boolean(anchorEl)}
            onClose={handleMenuClose}
            anchorOrigin={{ vertical: 'bottom', horizontal: 'right' }}
            transformOrigin={{ vertical: 'top', horizontal: 'right' }}
          >
            <MenuItem disabled>
              <Typography variant="body2">
                {user?.first_name} {user?.last_name}
              </Typography>
            </MenuItem>
            <MenuItem disabled>
              <Typography variant="caption" color="text.secondary">
                {user?.email}
              </Typography>
            </MenuItem>
            <Divider />
            <MenuItem onClick={() => navigate('/settings/profile')}>
              <ListItemIcon>
                <AccountCircle fontSize="small" />
              </ListItemIcon>
              Profile
            </MenuItem>
            <MenuItem onClick={handleLogout}>
              <ListItemIcon>
                <Logout fontSize="small" />
              </ListItemIcon>
              Logout
            </MenuItem>
          </Menu>
        </Toolbar>
      </AppBar>
      <Box
        component="nav"
        sx={{ width: { sm: drawerWidth }, flexShrink: { sm: 0 }, displayPrint: 'none' }}
        aria-label="mailbox folders"
      >
        <Drawer
          variant="temporary"
          open={mobileOpen}
          onClose={handleDrawerToggle}
          ModalProps={{
            keepMounted: true, // Better open performance on mobile.
          }}
          sx={{
            display: { xs: 'block', sm: 'none' },
            displayPrint: 'none',
            '& .MuiDrawer-paper': { boxSizing: 'border-box', width: drawerWidth },
          }}
        >
          {drawer}
        </Drawer>
        <Drawer
          variant="permanent"
          data-testid="app-navigation"
          sx={{
            display: { xs: 'none', sm: 'block' },
            displayPrint: 'none',
            '& .MuiDrawer-paper': { boxSizing: 'border-box', width: drawerWidth },
          }}
          open
        >
          {drawer}
        </Drawer>
      </Box>
      <Box
        component="main"
        sx={{
          flexGrow: 1,
          p: 3,
          width: { sm: `calc(100% - ${drawerWidth}px)` },
          minHeight: '100vh',
          backgroundColor: 'background.default',
          '@media print': { p: 0, width: '100%', minHeight: 'auto', backgroundColor: 'transparent' },
        }}
      >
        <Toolbar sx={{ displayPrint: 'none' }} />
        <Box sx={{ displayPrint: 'none' }}>
          <Breadcrumbs />
        </Box>
        <Outlet />
      </Box>
    </Box>
  );
};