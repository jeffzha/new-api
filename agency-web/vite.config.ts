import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  base: '/agency/',
  server: {
    proxy: {
      '/agency/api': 'http://127.0.0.1:3201',
      '/agency/sso': 'http://127.0.0.1:3201',
    },
  },
})
