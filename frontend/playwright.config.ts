import { defineConfig, devices } from '@playwright/test';

// Auth state written by global-setup; Playwright reads at context creation.
const STORAGE_STATE = '/tmp/kanban-test-storage-state.json';
const STORAGE_STATE_ISOLATED = '/tmp/kanban-test-storage-state.json.isolated';

// Specs that mutate server-global state (/api/config, providers, installed
// extensions, task concurrency) run against the second server on 3001, serialized.
// This array is the single source of truth: the chromium lane's testMatch below is
// derived from it, so the two lanes can never drift out of sync.
const ISOLATED_SPECS = [
  'extension-frame-channel',
  // Installs a translation pack from the registry fixture on the lane server — and
  // the offer it tests only exists in the no-pack state, so it cannot share a
  // server whose extension list someone else is mutating.
  'i18n-first-run-offer',
  // 13 tests of iframes, preview tokens and a live file listing — the first things to slip when
  // the lane server is answering six workers at once.
  'html-preview',
  'extensions-danger-permissions',
  'mode-select',
  'preferences-hide-model-badge',
  'preferences-keyboard',
  'provider-task-concurrency',
  'settings-extension-dependency-cache',
  'settings-provider',
  'settings-sessions',
];

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // Measured per full run: 6 workers ≈ 2.7 min with 1-4 failures that rotate between runs; 4 workers
  // ≈ 3.0-3.4 min with 0-1. They are all "waiting for an executor turn", and every worker queues
  // turns on the one lane server. Slower and boring beats faster and red.
  workers: 4,
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-teardown.ts',
  reporter: 'list',
  // Median test is ~2 s and p90 ~8 s; anything near a minute is a broken test, and
  // a long ceiling just turns a dead spec into a long dead spec.
  // 30s was tight for the tests that wait on an executor turn: every worker shares one
  // server, so a turn queues behind the other workers'. Timeouts cost nothing when green.
  timeout: 45000,
  expect: {
    // A task turn queues behind every other worker's turn on the one shared server, so
    // assertions that wait on executor output need slack; they return as soon as they hold.
    timeout: 15000,
  },
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
      testMatch: new RegExp(`/tests/e2e/(?!${ISOLATED_SPECS.join('|')}).*\\.spec\\.ts$`),
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          storageState: STORAGE_STATE_ISOLATED,
      },
      // Config/state-modifying suites on isolated server; serialized to avoid races
      testMatch: ISOLATED_SPECS.map((s) => `**/${s}.spec.ts`),
      fullyParallel: false,
      workers: 1,
    },
  ],
});
