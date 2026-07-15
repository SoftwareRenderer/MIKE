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
      testMatch: /\/tests\/e2e\/(?!model-selector|model-switch|settings-redesign|debug-cards|settings-model-delete|settings-reasoning-effort|reasoning-effort|sandbox-toggle|sandbox-tool-calls|sandbox-project-terminal|settings-power).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          storageState: STORAGE_STATE_ISOLATED,
      },
      // Config-modifying suites on isolated server; serialized to avoid races
      testMatch: ['**/model-selector.spec.ts', '**/model-switch.spec.ts', '**/settings-redesign.spec.ts', '**/debug-cards.spec.ts', '**/settings-model-delete.spec.ts', '**/settings-reasoning-effort.spec.ts', '**/reasoning-effort.spec.ts', '**/sandbox-toggle.spec.ts', '**/sandbox-tool-calls.spec.ts', '**/sandbox-project-terminal.spec.ts', '**/default-plan-mode.spec.ts', '**/settings-power.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
