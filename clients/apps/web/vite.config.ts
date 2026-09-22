import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev: the Vite server proxies /ws to a locally running daemon.
// Build: output lands in the Go embed directory, committed as a bundle.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/ws': { target: 'https://127.0.0.1:7432', ws: true, secure: false },
    },
  },
  build: {
    outDir: '../../../internal/webui/web',
    assetsDir: 'assets',
    sourcemap: false,
    target: 'es2022',
    emptyOutDir: true,
  },
});
