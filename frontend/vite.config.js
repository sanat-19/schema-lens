import { defineConfig } from 'vite';

// The backend (go run ./backend serve) answers /api on its own port; Vite
// forwards those requests to it, so the page and the API share an origin
// and the backend's cross-site guard works unchanged.
//
// changeOrigin stays false: the backend compares the browser's Origin with
// the Host header, so the Host has to be the one the browser used.
const api = process.env.SCHEMALENS_API || 'http://127.0.0.1:8080';
const proxy = { '/api': { target: api, changeOrigin: false } };

export default defineConfig({
  server: { port: 5173, strictPort: true, open: true, proxy },
  preview: { port: 4173, strictPort: true, proxy },
});
