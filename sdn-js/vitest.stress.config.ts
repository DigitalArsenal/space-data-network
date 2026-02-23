/**
 * Vitest configuration for stress tests only.
 *
 * Run with: npx vitest run --config vitest.stress.config.ts
 */
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    globals: true,
    environment: 'node',
    include: ['src/**/*.stress.test.ts'],
    testTimeout: 4 * 60 * 60 * 1000, // 4 hours
    hookTimeout: 60 * 1000, // 1 minute for setup/teardown
  },
});
