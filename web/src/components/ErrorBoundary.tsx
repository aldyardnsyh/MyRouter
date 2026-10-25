import { Component, type ErrorInfo, type ReactNode } from 'react';

interface Props {
  children: ReactNode;
  /** Optional label used in the console report (e.g. the page name). */
  scope?: string;
}

interface State {
  error: Error | null;
}

/**
 * ErrorBoundary keeps a rendering failure inside one page. Without it React 18
 * unmounts the whole tree, which is what produced the "blank white screen with
 * no sidebar" report: a single TypeError on a null API list took down the shell.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Keep the detail in the console for bug reports, never in the UI.
    console.error(`[myrouter] render error${this.props.scope ? ` (${this.props.scope})` : ''}`, error, info.componentStack);
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="mx-auto flex max-w-xl flex-col gap-3 rounded-card border border-border bg-bg p-6">
        <h1 className="text-h3 text-text">This page hit an error</h1>
        <p className="text-[12.5px] text-muted">
          Data yang diterima tidak sesuai bentuk yang diharapkan. Halaman lain tetap bisa dipakai.
        </p>
        <pre className="max-h-40 overflow-auto rounded-control border border-border bg-surface p-3 font-mono text-[11px] text-danger">
          {String(this.state.error?.message ?? this.state.error)}
        </pre>
        <div className="flex gap-2">
          <button
            type="button"
            onClick={() => this.setState({ error: null })}
            className="rounded-control border border-border bg-bg px-3 py-1.5 text-[12.5px] font-medium text-text hover:bg-surface-2"
          >
            Try again
          </button>
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="rounded-control border border-accent bg-accent px-3 py-1.5 text-[12.5px] font-medium text-white hover:bg-bg hover:text-text hover:border-border"
          >
            Reload
          </button>
        </div>
      </div>
    );
  }
}
