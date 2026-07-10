import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from "path"

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  base: '/static/',
  build: {
    outDir: path.resolve(__dirname, '..', 'static'),
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      '/api':       { target: 'http://localhost:8000', changeOrigin: true },
      '/v1':        { target: 'http://localhost:8000', changeOrigin: true },
    }
  }
})
