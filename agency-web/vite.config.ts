import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

function agencyBaseRedirect() {
  return {
    name: 'agency-base-redirect',
    configureServer(server: {
      middlewares: {
        use: (handler: (req: { url?: string }, res: { statusCode: number; setHeader: (name: string, value: string) => void; end: () => void }, next: () => void) => void) => void
      }
    }) {
      server.middlewares.use((req, res, next) => {
        if (req.url === '/agency' || req.url === '/agency/') {
          res.statusCode = 302
          res.setHeader('Location', '/agency/index.html')
          res.end()
          return
        }
        next()
      })
    },
  }
}

export default defineConfig({
  plugins: [agencyBaseRedirect(), react()],
  base: '/agency/',
  server: {
    proxy: {
      '/agency/api': 'http://127.0.0.1:3201',
      '/agency/sso': 'http://127.0.0.1:3201',
    },
  },
})
