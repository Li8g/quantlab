import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// Same-origin model (docs/frontend-promote-retire-v1.md §2): the Go side
// has no CORS middleware. In dev we proxy /api to the saas server so the
// browser sees one origin; in prod the build is served by saas via
// go:embed, also same-origin.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', changeOrigin: true },
    },
  },
})
