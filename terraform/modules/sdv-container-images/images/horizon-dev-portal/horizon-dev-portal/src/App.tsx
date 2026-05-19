// Copyright (c) 2024-2026 Accenture, All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
import { useEffect, useState } from 'react';
import {
  Box,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  Toolbar,
  AppBar,
  Typography,
  useMediaQuery,
  CircularProgress,
} from '@mui/material';
import { useTheme } from '@mui/material/styles';
import Tooltip from '@mui/material/Tooltip';
import MenuIcon from '@mui/icons-material/Menu';
import Brightness4Icon from '@mui/icons-material/Brightness4';
import Brightness7Icon from '@mui/icons-material/Brightness7';
import AdminPanelSettingsIcon from '@mui/icons-material/AdminPanelSettings';
import HomeIcon from '@mui/icons-material/Home';
import LogoutIcon from '@mui/icons-material/Logout';
import ViewModuleIcon from '@mui/icons-material/ViewModule';
import {
  BrowserRouter,
  Routes,
  Route,
  Navigate,
  Link,
  Outlet,
  useLocation,
} from 'react-router-dom';
import { ThemeModeProvider, useThemeMode } from './ThemeModeProvider';
import { authService } from './utils/auth';
import { LoginPage } from './pages/LoginPage';
import { WelcomePage } from './pages/WelcomePage';
import { AdminLayout } from './pages/admin/AdminLayout';
import { ModulesTab } from './pages/admin/ModulesTab';
import { SettingsTab } from './pages/admin/SettingsTab';
import { ModulePage } from './pages/ModulePage';
import { apiMm } from './utils/api';
import type { ModuleResponse, StatusResponse } from './types';
import { isReady } from './moduleStatus';
import { HORIZON_LOGO_SRC } from './constants';
import { getRouterBasename } from './utils/publicPath';

const drawerWidth = 260;

async function fetchReadyModuleNames(): Promise<string[]> {
  const r = await apiMm('/modules');
  if (!r.ok) {
    return [];
  }
  const list = (await r.json()) as ModuleResponse[];
  const ready: string[] = [];
  for (const m of list) {
    if (!m.enabled) {
      continue;
    }
    const sr = await apiMm(`/modules/${encodeURIComponent(m.name)}/status`);
    if (!sr.ok) {
      continue;
    }
    const st = (await sr.json()) as StatusResponse;
    if (isReady(m.enabled, st)) {
      ready.push(m.name);
    }
  }
  return ready.sort((a, b) => a.localeCompare(b));
}

function useReadyModules(): string[] {
  const [mods, setMods] = useState<string[]>([]);
  useEffect(() => {
    let cancelled = false;
    async function tick() {
      const names = await fetchReadyModuleNames();
      if (!cancelled) {
        setMods(names);
      }
    }
    void tick();
    const id = window.setInterval(() => void tick(), 12000);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, []);
  return mods;
}

function ShellLayout() {
  const [mobileOpen, setMobileOpen] = useState(false);
  const { darkMode, toggleTheme } = useThemeMode();
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down('sm'));
  const location = useLocation();
  const readyMods = useReadyModules();

  const drawer = (
    <Box>
      <Toolbar sx={{ gap: 1 }}>
        <Box
          component="img"
          src={HORIZON_LOGO_SRC}
          alt=""
          sx={{ height: 36, width: 'auto' }}
        />
        <Typography variant="h6" noWrap>
          Developer Portal
        </Typography>
      </Toolbar>
      <Divider />
      <List>
        <ListItemButton
          component={Link}
          to="/"
          selected={location.pathname === '/' || location.pathname === ''}
          onClick={() => isMobile && setMobileOpen(false)}
        >
          <ListItemIcon>
            <HomeIcon />
          </ListItemIcon>
          <ListItemText primary="Welcome" />
        </ListItemButton>
        <ListItemButton
          component={Link}
          to="/admin/modules"
          selected={location.pathname.startsWith('/admin')}
          onClick={() => isMobile && setMobileOpen(false)}
        >
          <ListItemIcon>
            <AdminPanelSettingsIcon />
          </ListItemIcon>
          <ListItemText primary="Administration" />
        </ListItemButton>
        {readyMods.map((name) => (
          <ListItemButton
            key={name}
            component={Link}
            to={`/module/${encodeURIComponent(name)}`}
            selected={location.pathname === `/module/${encodeURIComponent(name)}`}
            onClick={() => isMobile && setMobileOpen(false)}
          >
            <ListItemIcon>
              <ViewModuleIcon />
            </ListItemIcon>
            <ListItemText primary={name} />
          </ListItemButton>
        ))}
      </List>
    </Box>
  );

  return (
    <Box sx={{ display: 'flex', minHeight: '100vh' }}>
        <AppBar
          position="fixed"
          sx={{ zIndex: (t) => t.zIndex.drawer + 1 }}
        >
          <Toolbar>
            <IconButton
              color="inherit"
              edge="start"
              onClick={() => setMobileOpen(!mobileOpen)}
              sx={{ mr: 2, display: { sm: 'none' } }}
            >
              <MenuIcon />
            </IconButton>
            <Box
              component={Link}
              to="/"
              sx={{
                flexGrow: 1,
                display: 'flex',
                alignItems: 'center',
                gap: 1,
                minWidth: 0,
                textDecoration: 'none',
                color: 'inherit',
              }}
            >
              <Box
                component="img"
                src={HORIZON_LOGO_SRC}
                alt=""
                sx={{ height: 34, width: 'auto', flexShrink: 0 }}
              />
              <Typography variant="h6" component="span" noWrap>
                Horizon Developer Portal
              </Typography>
            </Box>
            <Typography variant="body2" sx={{ mr: 2, display: { xs: 'none', sm: 'block' } }}>
              {authService.getUsername()}
            </Typography>
            <Tooltip title={darkMode ? 'Light mode' : 'Dark mode'}>
              <IconButton
                color="inherit"
                onClick={toggleTheme}
                aria-label={darkMode ? 'Switch to light mode' : 'Switch to dark mode'}
              >
                {darkMode ? <Brightness7Icon /> : <Brightness4Icon />}
              </IconButton>
            </Tooltip>
            <IconButton color="inherit" onClick={() => authService.logout()} aria-label="logout">
              <LogoutIcon />
            </IconButton>
          </Toolbar>
        </AppBar>
        <Box component="nav" sx={{ width: { sm: drawerWidth }, flexShrink: { sm: 0 } }}>
          <Drawer
            variant="temporary"
            open={mobileOpen}
            onClose={() => setMobileOpen(false)}
            ModalProps={{ keepMounted: true }}
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
            mt: 8,
          }}
        >
          <Outlet />
        </Box>
      </Box>
  );
}

function RequireAuth({ children }: { children: React.ReactNode }) {
  const [ok, setOk] = useState<boolean | null>(null);
  useEffect(() => {
    authService
      .init()
      .then(setOk)
      .catch(() => setOk(false));
  }, []);
  if (ok === null) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="100vh">
        <CircularProgress />
      </Box>
    );
  }
  if (!ok) {
    return <Navigate to="/login" replace />;
  }
  return <>{children}</>;
}

export default function App() {
  return (
    <ThemeModeProvider>
      <BrowserRouter basename={getRouterBasename()}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          element={
            <RequireAuth>
              <ShellLayout />
            </RequireAuth>
          }
        >
          <Route path="/" element={<WelcomePage />} />
          <Route path="/admin" element={<AdminLayout />}>
            <Route index element={<Navigate to="modules" replace />} />
            <Route path="modules" element={<ModulesTab />} />
            <Route path="settings" element={<SettingsTab />} />
          </Route>
          <Route path="/module/:name" element={<ModulePage />} />
        </Route>
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
      </BrowserRouter>
    </ThemeModeProvider>
  );
}
