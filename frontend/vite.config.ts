import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  plugins: [react()],
  server: { strictPort: true, port: 5173, proxy: { '/api': 'http://localhost:8080', '/demo/docs': 'http://localhost:8080' } },
  test: {
    environment: 'jsdom',
    setupFiles: ['./test/setup.ts'],
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.{ts,tsx}', 'src/main.tsx'],
      reporter: ['text', 'json-summary', 'lcov'],
      // Строгий порог > 80% проверяет общий отчёт без округления.
      thresholds: { lines: 80 },
    },
  },
});
