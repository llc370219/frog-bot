import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// npm run dev 时把 /api 转发给 Go 后端（默认 127.0.0.1:8080）。
const api = process.env.FROGBOT_API || 'http://127.0.0.1:8080';

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { target: 'es2022' },
  server: { port: 5173, proxy: { '/api': { target: api, changeOrigin: true } } },
});
