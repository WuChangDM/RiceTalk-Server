/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  base: '/admin/',
  plugins: [react()],
  server: {
    port: 5174,
    host: true,
    proxy: {
      // Admin routes are mounted on the dedicated admin port (9090) by default,
      // not on the main API port (8080). Route admin requests to 9090.
      '/api/admin': {
        target: 'http://localhost:9090',
        changeOrigin: true,
      },
      '/api/auth': {
        target: 'http://localhost:9090',
        changeOrigin: true,
      },
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test/setup.ts',
    exclude: ['node_modules', 'e2e'],
    coverage: {
      provider: 'v8',
      lines: 70,
      functions: 70,
      branches: 70,
      statements: 70,
      exclude: [
        'node_modules/',
        'src/**/*.d.ts',
        'src/main.tsx',
        'src/vite-env.d.ts',
        'dist/',
        'build/',
        'src/test/**',
      ],
      reporter: ['text', 'json', 'html', 'lcov'],
    },
  },
})
