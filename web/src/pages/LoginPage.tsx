import { useRef, useState, type FormEvent } from 'react';
import { LockKeyhole, User } from 'lucide-react';
import { LogoWordmark } from '../components/Logo';
import { Button } from '../components/ui/Button';
import { Field, Input, PasswordInput } from '../components/ui/Field';
import { useAuth } from '../lib/auth';
import { ApiError } from '../lib/api';

// Login: centered 400px card on surface, logo above (PRD §6.1).
export function LoginPage() {
  const { login } = useAuth();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setError('');
    setLoading(true);
    try {
      await login(username.trim(), password);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Sign in failed');
      // Keep the typed value and put the caret back in the password field so a
      // retry does not require clearing or re-clicking the input.
      passwordRef.current?.focus();
      passwordRef.current?.select();
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-surface px-4">
      <div className="w-full max-w-[400px]">
        <div className="rounded-card border border-border bg-bg p-6">
          <LogoWordmark size={32} />
          <h1 className="mt-5 text-h2 text-text">Sign in to MyRouter</h1>
          <p className="mt-1 text-[12.5px] text-muted">Admin console for your routing gateway.</p>

          <form className="mt-5 flex flex-col gap-3.5" onSubmit={onSubmit}>
            <Field label="Username" htmlFor="username">
              <Input
                id="username"
                autoFocus
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                prefix={<User size={15} className="text-muted" />}
                placeholder="admin"
                required
              />
            </Field>
            <Field label="Password" htmlFor="password">
              <PasswordInput
                ref={passwordRef}
                id="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                prefix={<LockKeyhole size={15} className="text-muted" />}
                placeholder="••••••••"
                required
              />
            </Field>
            {error && (
              <p role="alert" className="rounded-control border border-danger/30 bg-danger-soft px-3 py-2 text-[12px] text-danger">
                {error}
              </p>
            )}
            <Button type="submit" variant="primary" loading={loading} className="mt-1 w-full">
              Sign in
            </Button>
          </form>
        </div>
        <p className="mt-5 text-center text-[12px] text-muted">One endpoint. Every model.</p>
      </div>
    </div>
  );
}
