/// <reference types="vitest" />
import { defineConfig } from 'vite'

export default defineConfig({
  server: {
    proxy: {
      '/_redirect': 'http://localhost:8079/',
      '/_rpc': 'http://localhost:8079/',
      '/_config': 'http://localhost:8079/',
      // Below are for the Mastodon built-in test server.
      '/oauth': 'http://localhost:8079/',
      '/api': 'http://localhost:8079/'
    },
  },

  test: {
    browser: {
      enabled: true,
      provider: 'playwright',
      headless: true,
      instances: [
        { browser: 'chromium' },
      ],
    },
  },

})