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
      // Exclude config/state-modifying suites (run on isolated server)
      testMatch: /\/tests\/e2e\/(?!settings-provider|settings-power|settings-model-pricing|settings-agent-provider|create-task-agent-mode|debug-cards|mode-select|cost-badge|preferences-keyboard|preferences-hide-model-badge|auto-summarize-proceed|extensions-danger-permissions|extensions-install-confirm|sandbox-memory|sandbox-config-memory-unlimited|provider-task-concurrency|lsp-sandbox-memory|pause-after-restart|subtask-parallel).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          storageState: STORAGE_STATE_ISOLATED,
      },
      // Config/state-modifying suites on isolated server; serialized to avoid races
      testMatch: ['**/settings-provider.spec.ts', '**/settings-power.spec.ts', '**/settings-model-pricing.spec.ts', '**/settings-agent-provider.spec.ts', '**/create-task-agent-mode.spec.ts', '**/debug-cards.spec.ts', '**/mode-select.spec.ts', '**/cost-badge.spec.ts', '**/preferences-keyboard.spec.ts', '**/preferences-hide-model-badge.spec.ts', '**/auto-summarize-proceed.spec.ts', '**/extensions-danger-permissions.spec.ts', '**/extensions-install-confirm.spec.ts', '**/sandbox-memory.spec.ts', '**/sandbox-config-memory-unlimited.spec.ts', '**/provider-task-concurrency.spec.ts', '**/lsp-sandbox-memory.spec.ts', '**/pause-after-restart.spec.ts', '**/subtask-parallel.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
