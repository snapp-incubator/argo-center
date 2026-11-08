import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// In dev, the Go server runs on :8080 and Vite proxies API calls to it.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://localhost:8080',
      '/auth': 'http://localhost:8080',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The main chunk is mostly Mantine + React (~175 kB gzipped); pages load lazily.
    chunkSizeWarningLimit: 700,
  },
});
