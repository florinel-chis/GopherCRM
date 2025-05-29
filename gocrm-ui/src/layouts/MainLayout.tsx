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
  Assignment,
  Task,
  ExpandLess,
  ExpandMore,
  Settings,
  Key,
  Logout,
  AccountCircle,
  ContactPhone,
  ChevronLeft,
  Tune,
} from '@mui/icons-material';
import { useAuth } from '@/hooks/useAuth';

const drawerWidth = 280;

interface NavItem {
  title: string;
  path?: string;
  icon: React.ReactNode;
  children?: NavItem[];
  roles?: string[];
}

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
    roles: ['admin', 'sales'],
  },
  {
    title: 'Customers',
    path: '/customers',
    icon: <Business />,
    roles: ['admin', 'sales', 'support'],
  },
  {
    title: 'Tickets',
    path: '/tickets',
    icon: <Assignment />,
    roles: ['admin', 'support'],
  },
  {
    title: 'Tasks',
    path: '/tasks',
    icon: <Task />,
  },
  {
    title: 'Users',
    path: '/users',
    icon: <People />,
    roles: ['admin'],
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
        roles: ['admin'],
      },
    ],
  },
];

export const MainLayout: React.FC = () => {
  const [mobileOpen, setMobileOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [anchorEl, setAnchorEl] = useState<null | HTMLElement>(null);
  const navigate = useNavigate();
  const { user, logout } = useAuth();

  const handleDrawerToggle = () => {
    setMobileOpen(!mobileOpen);
  };

  const handleSettingsToggle = () => {
    setSettingsOpen(!settingsOpen);
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
    if (!item.roles || !user) return true;
    return item.roles.includes(user.role);
  };

  const drawer = (
    <Box>
      <Toolbar>
        <Typography variant="h6" noWrap component="div">
          {import.meta.env.VITE_APP_NAME || 'GoCRM'}
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
            return (
              <React.Fragment key={item.title}>
                <ListItemButton onClick={handleSettingsToggle}>
                  <ListItemIcon>{item.icon}</ListItemIcon>
                  <ListItemText primary={item.title} />
                  {settingsOpen ? <ExpandLess /> : <ExpandMore />}
                </ListItemButton>
                <Collapse in={settingsOpen} timeout="auto" unmountOnExit>
                  <List component="div" disablePadding>
                    {item.children.map((child) => {
                      if (!canViewNavItem(child)) return null;
                      return (
                        <ListItemButton
                          key={child.title}
                          sx={{ pl: 4 }}
                          onClick={() => {
                            navigate(child.path!);
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
                  navigate(item.path!);
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

  return (
    <Box sx={{ display: 'flex' }}>
      <CssBaseline />
      <AppBar
        position="fixed"
        sx={{
          width: { sm: `calc(100% - ${drawerWidth}px)` },
          ml: { sm: `${drawerWidth}px` },
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
        sx={{ width: { sm: drawerWidth }, flexShrink: { sm: 0 } }}
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
            '& .MuiDrawer-paper': { boxSizing: 'border-box', width: drawerWidth },
          }}
        >
          {drawer}
        </Drawer>
        <Drawer
          variant="permanent"
          sx={{
            display: { xs: 'none', sm: 'block' },
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
        }}
      >
        <Toolbar />
        <Breadcrumbs />
        <Outlet />
      </Box>
    </Box>
  );
};