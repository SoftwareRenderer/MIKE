import { defineConfig, devices } from '@playwright/test';

// Auth state written by global-setup; Playwright reads at context creation.
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  workers: 4,
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-setup.ts',
  reporter: 'list',
  timeout: 60000,
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'off',
  },
  projects: [
    {
      name: 'chromium',
      use: {
          ...devices['Desktop Chrome'],
          storageState: STORAGE_STATE,
      },
      // Exclude config-modifying suites (run on isolated server)
      testMatch: /\/tests\/e2e\/(?!settings-provider|settings-power|settings-model-pricing|debug-cards|mode-select).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          storageState: STORAGE_STATE_ISOLATED,
      },
      // Config-modifying suites on isolated server; serialized to avoid races
      testMatch: ['**/settings-provider.spec.ts', '**/settings-power.spec.ts', '**/settings-model-pricing.spec.ts', '**/debug-cards.spec.ts', '**/mode-select.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
