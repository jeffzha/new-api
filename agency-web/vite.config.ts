import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

function agencyDevEntry() {
  return {
    name: 'agency-dev-entry',
    configureServer(server: {
      middlewares: {
        use: (handler: (req: { url?: string }, _res: unknown, next: () => void) => void) => void
      }
    }) {
      server.middlewares.use((req, _res, next) => {
        if (req.url) {
          const url = new URL(req.url, 'http://localhost')
          if (url.pathname === '/agency' || url.pathname === '/agency/') {
            req.url = `/agency/index.html${url.search}`
          }
        }
        next()
      })
    },
  }
}

export default defineConfig({
  plugins: [agencyDevEntry(), react()],
  base: '/agency/',
  server: {
    strictPort: true,
    proxy: {
      '/agency/api': 'http://127.0.0.1:3201',
      '/agency/sso': 'http://127.0.0.1:3201',
    },
  },
})
