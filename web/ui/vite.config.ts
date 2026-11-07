import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// The build goes into ../static, which the Go server embeds (see ../web.go). Paths are
// relative because the server serves the dashboard under /dashboard/.
// In development, `npm run dev` serves the page itself and sends API calls to the
// Go server, so the cookie and the CSRF token behave as they do in production.
const api = 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: '../static', emptyOutDir: true, sourcemap: false, target: 'es2022' },
  server: {
    port: 5173,
    proxy: { '/auth': api, '/orders': api, '/alerts': api, '/health': api },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.{ts,tsx}'], setupFiles: ['src/test/setup.ts'], css: false, testTimeout: 20_000 },
});
