import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The Go server embeds the build; asset URLs stay relative so the panel can
// live under any path (MRSMITH_PANEL_PATH) via <base href>.
export default defineConfig({
  base: './',
  plugins: [react()],
  build: {
    outDir: '../backend/internal/webui/dist',
    emptyOutDir: true,
    assetsInlineLimit: 0, // CSP forbids data: fonts and scripts
    sourcemap: false,
    chunkSizeWarningLimit: 900,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/r/': 'http://127.0.0.1:8080',
      '/f/': 'http://127.0.0.1:8080',
    },
  },
})
