import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from '@rsbuild/core'
import { pluginReact } from '@rsbuild/plugin-react'
import { pluginTailwindcss } from '@rsbuild/plugin-tailwindcss'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

/**
 * Builds the API documentation page as a fully static, backend-independent
 * bundle. `assetPrefix` is set via ASSET_PREFIX (default `/apidocs/`) so the
 * output can be mounted at a non-conflicting path behind a reverse proxy.
 */
export default defineConfig({
  plugins: [pluginReact(), pluginTailwindcss({ optimize: true })],
  source: {
    entry: {
      index: './src/standalone/docs-standalone.tsx',
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  html: {
    template: './src/standalone/template.html',
    title: 'Nexus Reach Seedance 2.0 API Docs',
  },
  output: {
    minify: true,
    target: 'web',
    assetPrefix: process.env.ASSET_PREFIX || '/apidocs/',
    distPath: {
      root: 'dist-docs',
    },
  },
  // Split React and other vendor code into cacheable chunks so repeat visits
  // and parallel downloads stay fast.
  splitChunks: {
    preset: 'default',
    cacheGroups: {
      'vendor-react': {
        test: /node_modules[\\/](react|react-dom)[\\/]/,
        name: 'vendor-react',
        chunks: 'all',
        priority: 10,
        enforce: true,
      },
    },
  },
  performance: {
    removeConsole: ['log'],
    buildCache: false,
  },
})
