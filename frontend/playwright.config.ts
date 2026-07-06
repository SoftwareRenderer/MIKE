import { defineConfig, devices } from '@playwright/test';
import fs from 'node:fs';

// Read auth info written by global-setup (session cookie + CSRF token)
function readAuth(path: string) {
    try { return JSON.parse(fs.readFileSync(path, 'utf-8')); } catch { return null; }
}
const authInfo = readAuth('/tmp/kanban-test-auth.json');
const authInfoIso = readAuth('/tmp/kanban-test-auth.json.isolated');

function makeStorageState(info: any) {
    if (!info) return undefined;
    // Ensure cookies have valid expires field (Playwright requires float timestamp)
    const cookies = (info.cookies || []).map((c: any) => ({
        ...c,
        expires: c.expires ?? Math.floor(Date.now() / 1000) + 86400 * 365, // 1 year from now
    }));
    return { cookies, origins: [] };
}

export default defineConfig({
  testDir: './tests/e2e',
  fullyParallel: true, // Enable parallel execution within each project
  forbidOnly: !!process.env.CI,
  workers: 2, // Workers for parallel tests (reduced to avoid SQLite contention)
  globalSetup: './tests/global-setup.ts',
  globalTeardown: './tests/global-setup.ts',
  reporter: 'list',
  timeout: 120000, // 120s timeout per test
  use: {
    baseURL: 'http://localhost:3000',
    trace: 'off',
  },
  projects: [
    {
      name: 'chromium',
      use: {
          ...devices['Desktop Chrome'],
          ...(authInfo ? { storageState: makeStorageState(authInfo) } : {}),
      },
      // All test files EXCEPT config-modifying suites (those run on isolated server)
      testMatch: /\/tests\/e2e\/(?!model-selector|model-switch|settings-redesign|debug-cards|settings-model-delete|settings-reasoning-effort|reasoning-effort|sandbox-toggle|sandbox-tool-calls|sandbox-project-terminal|settings-power).*\.spec\.ts$/,
    },
    {
      name: 'isolated',
      use: {
          ...devices['Desktop Chrome'],
          baseURL: 'http://localhost:3001',
          ...(authInfoIso ? { storageState: makeStorageState(authInfoIso) } : {}),
      },
      // Config-modifying suites run on isolated server (port 3001, own DB)
      // Serialized to avoid races between suites that modify shared config
      testMatch: ['**/model-selector.spec.ts', '**/model-switch.spec.ts', '**/settings-redesign.spec.ts', '**/debug-cards.spec.ts', '**/settings-model-delete.spec.ts', '**/settings-reasoning-effort.spec.ts', '**/reasoning-effort.spec.ts', '**/sandbox-toggle.spec.ts', '**/sandbox-tool-calls.spec.ts', '**/sandbox-project-terminal.spec.ts', '**/default-plan-mode.spec.ts', '**/settings-power.spec.ts'],
      fullyParallel: false,
      workers: 1,
    },
  ],
});
