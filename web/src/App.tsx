import { Navigate, Route, Routes } from 'react-router-dom';
import { AppShell } from './components/AppShell';
import { useAuth } from './lib/auth';
import { LoginPage } from './pages/LoginPage';
import { SetupPage } from './pages/SetupPage';
import { DashboardPage } from './pages/DashboardPage';
import { ProvidersPage } from './pages/ProvidersPage';
import { ModelsPage, ProviderDetailPage } from './pages/ModelsPage';
import { RoutesPage } from './pages/RoutesPage';
import { ApiKeysPage } from './pages/ApiKeysPage';
import { GuardPage } from './pages/GuardPage';
import { ActivityPage } from './pages/ActivityPage';
import { PlaygroundPage } from './pages/PlaygroundPage';
import { SettingsPage } from './pages/SettingsPage';

function LoadingScreen() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-bg">
      <div className="flex items-center gap-2 text-[13px] text-muted">
        <span className="h-2 w-2 animate-pulse rounded-full bg-accent" />
        Starting MyRouter…
      </div>
    </div>
  );
}

export function App() {
  const { status } = useAuth();

  if (status === 'loading') return <LoadingScreen />;
  if (status === 'needs-setup') return <SetupPage />;
  if (status === 'anonymous') return <LoginPage />;

  return (
    <Routes>
      <Route element={<AppShell />}>
        <Route index element={<DashboardPage />} />
        <Route path="providers" element={<ProvidersPage />} />
        <Route path="providers/:id" element={<ProviderDetailPage />} />
        <Route path="models" element={<ModelsPage />} />
        <Route path="routes" element={<RoutesPage />} />
        <Route path="api-keys" element={<ApiKeysPage />} />
        <Route path="guard" element={<GuardPage />} />
        <Route path="activity" element={<ActivityPage />} />
        <Route path="playground" element={<PlaygroundPage />} />
        <Route path="settings" element={<SettingsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
