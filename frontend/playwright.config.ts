import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: false, // Run all tests serially for deterministic results
  forbidOnly: !!process.env.CI,
  retries: 1, // Allow one retry for flaky tests
  workers: 1, // Single worker to avoid server overload
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-setup.ts',
  reporter: 'list',
  timeout: 60000, // 60s timeout per test
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'off',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
