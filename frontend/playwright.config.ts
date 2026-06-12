import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true, // Enable parallel execution within each project
  forbidOnly: !!process.env.CI,
  workers: 4, // Workers for parallel tests (serial tests use 1 worker internally)
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
      // All test files EXCEPT config-modifying suites (those run on isolated server)
      testMatch: /\/tests\/e2e\/(?!model-selector|settings-redesign).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: { ...devices['Desktop Chrome'], baseURL: 'http://localhost:3001' },
      // Config-modifying suites run on isolated server (port 3001, own DB)
      // Serialized to avoid races between suites that modify shared config
      testMatch: ['**/model-selector.spec.ts', '**/settings-redesign.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
