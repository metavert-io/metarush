import { defineConfig, type ProxyOptions } from 'vite';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

const backend = process.env.METARUSH_BACKEND ?? 'http://127.0.0.1:4841';

// The Go server checks the Host header (DNS-rebinding defense) and allows
// localhost:4840 / 127.0.0.1:4840 as dev origins, so the proxy must NOT rewrite it.
const proxied: ProxyOptions = {
  target: backend,
  changeOrigin: false,
  configure(proxy) {
    proxy.on('proxyRes', (res) => {
      // Keep Server-Sent Events flowing chunk by chunk through the dev proxy.
      if (String(res.headers['content-type'] ?? '').startsWith('text/event-stream')) {
        res.headers['cache-control'] = 'no-cache, no-transform';
        res.headers['x-accel-buffering'] = 'no';
        delete res.headers['content-length'];
      }
    });
  },
};

export default defineConfig({
  base: '/',
  plugins: [react(), tailwindcss()],
  server: {
    port: 4840,
    strictPort: true,
    host: '127.0.0.1',
    proxy: {
      '/api': proxied,
      '/media': proxied,
      '/auth': proxied,
    },
  },
  preview: {
    port: 4840,
    strictPort: true,
    host: '127.0.0.1',
    proxy: {
      '/api': proxied,
      '/media': proxied,
      '/auth': proxied,
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
    // Coverage builds (COVERAGE=1 browser runs, D195) keep maps beside the bundles (not linked from
    // them) and aren't minified (coverage maps back line by line), so web/scripts/coverage/report.mjs
    // can count the browser's coverage in web/src.
    sourcemap: process.env.METARUSH_WEB_SOURCEMAP === '1' ? 'hidden' : false,
    minify: process.env.METARUSH_WEB_SOURCEMAP !== '1',
    chunkSizeWarningLimit: 1200,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes('node_modules/remotion') || id.includes('node_modules/@remotion')) return 'remotion';
          if (id.includes('node_modules/react-dom') || id.includes('node_modules/react/') || id.includes('node_modules/scheduler')) return 'react';
          return undefined;
        },
      },
    },
  },
});
