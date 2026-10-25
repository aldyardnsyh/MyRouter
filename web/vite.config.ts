import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// SPA build is embedded into the Go binary via //go:embed (ADR-001).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    sourcemap: false,
    chunkSizeWarningLimit: 600,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/admin': 'http://localhost:20128',
      '/v1': 'http://localhost:20128',
      '/healthz': 'http://localhost:20128',
    },
  },
});
