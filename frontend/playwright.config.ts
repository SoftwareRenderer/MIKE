import { defineConfig, devices } from '@playwright/test';

// Auth state written by global-setup; Playwright reads at context creation.
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  workers: 2,
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-setup.ts',
  reporter: 'list',
  timeout: 120000,
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
      testMatch: /\/tests\/e2e\/(?!model-switch|debug-cards|settings-model-delete|reasoning-effort|settings-power|mode-select).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          storageState: STORAGE_STATE_ISOLATED,
      },
      // Config-modifying suites on isolated server; serialized to avoid races
      testMatch: ['**/model-switch.spec.ts', '**/debug-cards.spec.ts', '**/settings-model-delete.spec.ts', '**/reasoning-effort.spec.ts', '**/settings-power.spec.ts', '**/mode-select.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
