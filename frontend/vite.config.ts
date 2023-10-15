import { defineConfig } from 'vite'

export default defineConfig({
    server: {
        proxy: {
            '/_api': 'http://localhost:8079/',
        },
    }
})