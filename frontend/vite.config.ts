import { defineConfig } from 'vite'

export default defineConfig({
    server: {
        proxy: {
            '/list': 'http://localhost:8079/',
        },
    }
})