import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist', emptyOutDir: true, sourcemap: false,
    rollupOptions: { output: { manualChunks: { leaflet: ['leaflet'], react: ['react', 'react-dom', 'react-router-dom'] } } },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/healthz': 'http://localhost:8080',
      '/cal': 'http://localhost:8080',
    },
  },
})
