import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { test, expect } from '@playwright/test';
import { csrfFetch } from './helpers';

// This suite runs in the `isolated` Playwright project (see
// playwright.config.ts): installing the LSP extension mutates the shared
// extension store, and the isolated server is serialized.
//
// It needs gVisor (runsc) for the LSP sandbox — soft-skips when unavailable
// (same pattern as sandbox-memory.spec.ts).

const TS_WORKSPACE = '/tmp/kanban-test-lsp-mem';

function ensureTsWorkspace(): void {
  if (!existsSync(TS_WORKSPACE)) {
    mkdirSync(TS_WORKSPACE, { recursive: true });
    execSync('git init', { cwd: TS_WORKSPACE });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit --allow-empty -m "init"', {
      cwd: TS_WORKSPACE,
    });
  }
  // A minimal tsconfig + file (mirrors lsp-rename.spec.ts — no npm install
  // needed; tsserver works on plain files).
  writeFileSync(
    `${TS_WORKSPACE}/tsconfig.json`,
    `{\n  "compilerOptions": {"target": "ES2022", "module": "ESNext", "moduleResolution": "bundler", "strict": true, "noEmit": true},\n  "include": ["main.ts"]\n}\n`
  );
  writeFileSync(
    `${TS_WORKSPACE}/main.ts`,
    `export function getGreeting(): string {
\treturn "hello";
}
`
  );
}

async function ensureLspExtension(page: test['page'], name: string): Promise<void> {
  const existing = await csrfFetch(page, '/api/extensions', { method: 'GET' });
  const list: any[] = Array.isArray(existing.data) ? existing.data : [];
  if (list.some((e) => e.name === name)) {
    return;
  }
  // Install from the pinned fixture registry (SSE stream; resolves when the
  // stream ends — the first install also builds the container image, which
  // can be slow).
  const result = await csrfFetch(page, '/api/extensions/install', { method: 'POST', body: { name } });
  if (!result.ok) {
    throw new Error(`${name} extension install failed: ${result.status} ${JSON.stringify(result.data)}`);
  }
}

async function createProjectViaApi(page: test['page'], path: string): Promise<string> {
  const result = await csrfFetch(page, '/api/projects', {
    method: 'POST',
    body: { name: 'lsp-sandbox-memory-test', path },
  });
  if (!result.ok) {
    throw new Error(`Failed to create project: ${result.status} ${JSON.stringify(result.data)}`);
  }
  return (result.data as any).id as string;
}

async function deleteProjectViaApi(page: test['page'], projectId: string): Promise<void> {
  await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
}

test.describe.serial('LSP Sandbox Memory (editor footer tooltip)', () => {
  let projectId = '';

  test.afterAll(() => {
    rmSync(TS_WORKSPACE, { recursive: true, force: true });
    // Note: the LSP extension is intentionally left installed — other
    // isolated suites reuse it and the install is idempotent.
  });

  test('shows the LSP sandbox memory usage in the LSP status tooltip', async ({ page }) => {
    // gVisor probe — the LSP sandbox needs runsc.
    try {
      execSync('runsc --version', { stdio: 'ignore' });
    } catch {
      test.skip(true, 'gVisor (runsc) not available');
      return;
    }

    test.setTimeout(600000); // first image build + sandbox start can be slow

    ensureTsWorkspace();
    // Install before project navigation: the install builds the image and
    // pre-creates the sandbox, so the editor connection only starts the
    // language server.
    await ensureLspExtension(page, 'typescript-language-server');

    try {
      projectId = await createProjectViaApi(page, TS_WORKSPACE);
      await page.goto(`/?project=${projectId}`);
      await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });

      // Open the explorer view and wait for the file browser to load
      // (sandbox initialization can be slow).
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(async () => {
        const loadingVisible = await page.locator('.file-browser-loading').isVisible();
        const listVisible = await page.locator('.file-browser-list').isVisible().catch(() => false);
        const errorVisible = await page.locator('.file-browser-error').isVisible().catch(() => false);
        if (!loadingVisible && (listVisible || errorVisible)) {
          if (errorVisible) {
            throw new Error(`File browser error: ${await page.locator('.file-browser-error').textContent()}`);
          }
          if (!listVisible) {
            throw new Error('File browser finished loading but list is not visible');
          }
          return;
        }
        throw new Error('File browser still loading');
      }).toPass({ timeout: 60000, intervals: [2000] });

      // Open main.ts in the editor.
      const mainTsItem = page.locator('.file-browser-item', { hasText: 'main.ts' });
      await expect(mainTsItem).toBeVisible({ timeout: 10000 });
      await mainTsItem.click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
      await expect(page.locator('.editor-loading')).not.toBeVisible({ timeout: 10000 });

      // Wait for the LSP to be ready — the footer status button shows the
      // language name once the backend confirms $/lspReady.
      const lspBtn = page.locator('button[aria-label="LSP status"]');
      await expect(lspBtn).toHaveText('TypeScript', { timeout: 120000 });

      // Open the LSP status tooltip.
      await lspBtn.click();

      // The Sandbox Memory row appears with a one-shot fetch on open.
      const memRow = page.locator('.lsp-tooltip-row', { hasText: 'Sandbox Memory' });
      await expect(memRow).toBeVisible({ timeout: 10000 });
      await expect(async () => {
        const text = (await memRow.locator('.lsp-tooltip-value').innerText()).trim();
        const m = text.match(/^(\d+) \/ (\d+) MB$/);
        expect(m, `expected "N / L MB", got "${text}"`).not.toBeNull();
        expect(parseInt(m![1], 10), 'current usage should be > 0 while tsserver runs').toBeGreaterThan(0);
      }).toPass({ timeout: 30000, intervals: [2000] });
    } finally {
      if (projectId) {
        await deleteProjectViaApi(page, projectId).catch(() => {});
      }
    }
  });
});
