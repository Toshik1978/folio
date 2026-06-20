import { fileURLToPath, URL } from 'node:url';

import tailwindcss from '@tailwindcss/vite';
import vue from '@vitejs/plugin-vue';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: './dist',
    emptyOutDir: true,
  },
  test: {
    environment: 'happy-dom',
    include: ['src/**/*.spec.ts'],
    setupFiles: ['./src/test/setup.ts'],
    // The `json` test reporter (enabled in CI via the test:ci script) writes its
    // Jest-shaped results here; `default` prints to stdout and writes no file.
    outputFile: { json: 'test_status.json' },
    coverage: {
      provider: 'v8',
      reportsDirectory: 'coverage',
      // text → console summary; json-summary → coverage/coverage-summary.json
      // (parsed in CI for the coverage badge).
      reporter: ['text', 'json-summary', 'cobertura'],
      include: ['src/**/*.{ts,vue}'],
      exclude: ['src/**/*.spec.ts', 'src/test/**', 'src/main.ts', 'src/vite-env.d.ts'],
    },
  },
});
