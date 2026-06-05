import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: 1,
  workers: 6,
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-setup.ts',
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: /.*\.mobile\.spec\.ts/,
    },
    {
      name: 'mobile',
      use: {
        ...devices['Pixel 5'],
        viewport: { width: 390, height: 844 },
      },
      testMatch: /.*\.mobile\.spec\.ts/,
    },
  ],
});
