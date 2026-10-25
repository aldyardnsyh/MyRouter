import ReactDOM from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { App } from './App';
import { AuthProvider } from './lib/auth';
import { ThemeProvider } from './lib/theme';
import { ToastProvider } from './components/ui/Toast';
import { ErrorBoundary } from './components/ErrorBoundary';
import './index.css';

// StrictMode double-invokes effects in dev, which doubles the fetches and
// amplifies the skeleton flash during navigation. Disabled intentionally.
ReactDOM.createRoot(document.getElementById('root')!).render(
  <ErrorBoundary scope="app">
    <ThemeProvider>
      <ToastProvider>
        <BrowserRouter>
          <AuthProvider>
            <App />
          </AuthProvider>
        </BrowserRouter>
      </ToastProvider>
    </ThemeProvider>
  </ErrorBoundary>,
);
