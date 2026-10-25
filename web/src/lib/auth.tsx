import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { admin } from './api';

interface AuthState {
  status: 'loading' | 'anonymous' | 'authenticated' | 'needs-setup';
  username: string;
  version: string;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthState['status']>('loading');
  const [username, setUsername] = useState('');
  const [version, setVersion] = useState('0.1.0');

  const bootstrap = useCallback(async () => {
    try {
      const setup = await admin.setupStatus();
      setVersion(setup.version);
      if (setup.needs_setup) {
        setStatus('needs-setup');
        return;
      }
    } catch {
      // Fall through to the session probe; the login screen will surface errors.
    }
    try {
      const me = await admin.me();
      setUsername(me.username);
      setVersion(me.version);
      setStatus('authenticated');
    } catch {
      setStatus('anonymous');
    }
  }, []);

  useEffect(() => {
    void bootstrap();
  }, [bootstrap]);

  useEffect(() => {
    const onUnauthorized = () => setStatus('anonymous');
    window.addEventListener('myrouter:unauthorized', onUnauthorized);
    return () => window.removeEventListener('myrouter:unauthorized', onUnauthorized);
  }, []);

  const login = useCallback(async (u: string, p: string) => {
    const res = await admin.login(u, p);
    setUsername(res.username);
    setStatus('authenticated');
  }, []);

  const logout = useCallback(async () => {
    try {
      await admin.logout();
    } finally {
      setStatus('anonymous');
      setUsername('');
    }
  }, []);

  const refresh = useCallback(async () => {
    await bootstrap();
  }, [bootstrap]);

  const value = useMemo(
    () => ({ status, username, version, login, logout, refresh }),
    [status, username, version, login, logout, refresh],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider');
  return ctx;
}
