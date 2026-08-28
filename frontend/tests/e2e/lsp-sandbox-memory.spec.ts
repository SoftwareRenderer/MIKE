import { execSync } from 'node:child_process';
import { mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs';
import { test, expect, type Browser, type Page } from '@playwright/test';
import { csrfFetch } from './helpers';

// This suite runs in the `isolated` Playwright project (see
// playwright.config.ts): installing the LSP extension mutates the shared
// extension store, and the isolated server is serialized.
//
// It needs gVisor (runsc) for the LSP sandbox — soft-skips when unavailable
// (same pattern as sandbox-memory.spec.ts).
//
// The memory_limit option is HOST-OWNED for LSP extensions: every LSP
// extension's edit form shows it (default 0 = no limit, declared or not),
// and a saved value is applied LIVE to the running sandbox (cgroup write,
// no restart). Test 2 saves a limit while the sandbox keeps running through
// its grace period (the editor is closed to reach Settings, which the
// explorer view does not offer) and verifies the saved limit is in effect
// when the editor reconnects — proof the limit was applied live, not just
// persisted for the next sandbox start.

const TS_WORKSPACE = '/tmp/kanban-test-lsp-mem';
const EXT_NAME = 'typescript-language-server';
const EXT_DISPLAY = 'TypeScript/JavaScript Language Server';
const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const MEM_INPUT = '#edit-config-memory_limit';

// "N MB (no limit)" — the host's no-limit format (memory_limit_mb = 0).
const NO_LIMIT_RE = /^(\d+) MB \(no limit\)$/;
// "N / 512 MB" — the limited format with the exact saved limit.
const LIMITED_512_RE = /^(\d+) \/ 512 MB$/;

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

async function ensureLspExtension(page: Page, name: string): Promise<void> {
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

async function createProjectViaApi(page: Page, path: string): Promise<string> {
  const result = await csrfFetch(page, '/api/projects', {
    method: 'POST',
    body: { name: 'lsp-sandbox-memory-test', path },
  });
  if (!result.ok) {
    throw new Error(`Failed to create project: ${result.status} ${JSON.stringify(result.data)}`);
  }
  return (result.data as any).id as string;
}

async function deleteProjectViaApi(page: Page, projectId: string): Promise<void> {
  await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
}

// Open the TS workspace's file in the editor and wait for the file browser
// and CodeMirror to settle (sandbox initialization can be slow).
async function openTsEditor(page: Page, timeoutMs: number, projectId: string): Promise<void> {
  await page.goto(`/?project=${projectId}`);
  await expect(page.locator('.board')).toBeVisible({ timeout: 15000 });

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
  }).toPass({ timeout: timeoutMs, intervals: [2000] });

  const mainTsItem = page.locator('.file-browser-item', { hasText: 'main.ts' });
  await expect(mainTsItem).toBeVisible({ timeout: 10000 });
  await mainTsItem.click();
  await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 15000 });
  await expect(page.locator('.editor-loading')).not.toBeVisible({ timeout: 10000 });

  // Wait for the LSP to be ready — the footer status button shows the
  // language name once the backend confirms $/lspReady.
  const lspBtn = page.locator('button[aria-label="LSP status"]');
  await expect(lspBtn).toHaveText('TypeScript', { timeout: 120000 });
}

// Close the explorer view (back to the board, whose TopBar offers Settings).
async function closeExplorer(page: Page): Promise<void> {
  await page.locator('.explorer-view .close-btn[aria-label="Close branch view"]').click();
  await expect(page.locator('.explorer-view')).not.toBeVisible({ timeout: 5000 });
}

// Open the LSP status tooltip (fresh one-shot memory fetch on each open) and
// wait until the Sandbox Memory row matches pattern.
async function expectMemRow(page: Page, pattern: RegExp, label: string, timeoutMs: number): Promise<void> {
  const lspBtn = page.locator('button[aria-label="LSP status"]');
  await lspBtn.click();
  const memRow = page.locator('.lsp-tooltip-row', { hasText: 'Sandbox Memory' });
  await expect(memRow).toBeVisible({ timeout: 10000 });
  await expect(async () => {
    const text = (await memRow.locator('.lsp-tooltip-value').innerText()).trim();
    const m = text.match(pattern);
    expect(m, `expected "${label}", got "${text}"`).not.toBeNull();
    expect(parseInt(m![1], 10), 'current usage should be > 0 while tsserver runs').toBeGreaterThan(0);
  }).toPass({ timeout: timeoutMs, intervals: [2000] });
}

// Close the LSP status tooltip (toggle off).
async function closeLspTooltip(page: Page): Promise<void> {
  const lspBtn = page.locator('button[aria-label="LSP status"]');
  await lspBtn.click();
  await expect(page.locator('.lsp-tooltip')).not.toBeVisible({ timeout: 5000 });
}

// Open Settings → Extensions and the edit modal for the LSP extension.
async function openLspEditForm(page: Page): Promise<void> {
  await page.locator(SETTINGS_BTN).first().click();
  await page.click('text=Extensions');
  await expect(page.locator('.extensions-tab')).toBeVisible({ timeout: 5000 });

  const card = page.locator('.extension-card', { hasText: EXT_DISPLAY });
  await expect(card).toBeVisible({ timeout: 10000 });
  await card.locator('button[title="Edit"]').click();
  await expect(page.locator(MEM_INPUT)).toBeVisible({ timeout: 5000 });
}

async function saveEditForm(page: Page): Promise<void> {
  await page.locator('.config-modal button.btn-primary:has-text("Save")').click();
  await expect(page.locator(MEM_INPUT)).not.toBeVisible({ timeout: 10000 });
}

// Close the settings modal (the nested edit modal is already closed by Save).
async function closeSettings(page: Page): Promise<void> {
  await page.keyboard.press('Escape');
  await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
}

test.describe.serial('LSP Sandbox Memory (editor footer tooltip)', () => {
  let projectId = '';
  let skipReason = '';

  test.beforeAll(async ({ browser }: { browser: Browser }) => {
    // gVisor probe — the LSP sandbox needs runsc.
    try {
      execSync('runsc --version', { stdio: 'ignore' });
    } catch {
      skipReason = 'gVisor (runsc) not available';
      return;
    }

    const context = await browser.newContext({
      storageState: '/tmp/kanban-test-storage-state.json.isolated',
    });
    const page = await context.newPage();
    try {
      ensureTsWorkspace();
      // Install before project navigation: the install builds the image and
      // pre-creates the sandbox, so the editor connection only starts the
      // language server.
      await ensureLspExtension(page, EXT_NAME);
      projectId = await createProjectViaApi(page, TS_WORKSPACE);
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }: { browser: Browser }) => {
    const context = await browser.newContext({
      storageState: '/tmp/kanban-test-storage-state.json.isolated',
    });
    const page = await context.newPage();
    try {
      if (projectId) {
        await deleteProjectViaApi(page, projectId).catch(() => {});
      }
    } finally {
      await context.close();
      rmSync(TS_WORKSPACE, { recursive: true, force: true });
      // Note: the LSP extension is intentionally left installed — other
      // isolated suites reuse it and the install is idempotent.
    }
  });

  test('shows the default no-limit sandbox memory in the LSP status tooltip', async ({ page }) => {
    test.skip(!!skipReason, skipReason);
    test.setTimeout(600000); // first image build + sandbox start can be slow

    await openTsEditor(page, 60000, projectId);

    // The host default is 0 (no limit) — the row shows usage without a limit.
    await expectMemRow(page, NO_LIMIT_RE, 'N MB (no limit)', 30000);
  });

  test('applies an edited memory limit live after save', async ({ page }) => {
    test.skip(!!skipReason, skipReason);
    test.setTimeout(600000); // several editor reconnects (warm, but re-indexing)

    // 1. Editor + LSP ready; precondition: still unlimited (fresh install,
    //    default 0).
    await openTsEditor(page, 60000, projectId);
    await expectMemRow(page, NO_LIMIT_RE, 'N MB (no limit)', 30000);
    await closeLspTooltip(page);
    // Settings lives in the board view's TopBar, not the explorer view.
    // Closing the explorer releases the LSP client, but the sandbox stays
    // running through its grace period — which is exactly when the live
    // apply happens on save.
    await closeExplorer(page);

    // 2. Save a 512 MB limit via the host-owned field in the edit form.
    await openLspEditForm(page);
    // The form seeds from the effective sandbox_config value: unlimited (0)
    // renders as a blank field carrying the "No limit" placeholder.
    await expect(page.locator(MEM_INPUT)).toHaveValue('');
    await expect(page.locator(MEM_INPUT)).toHaveAttribute('placeholder', 'No limit');
    await page.locator(MEM_INPUT).fill('512');
    await saveEditForm(page);
    await closeSettings(page);

    // 3. Reconnect the editor: the sandbox was NOT recreated, so the 512 MB
    //    limit must have been applied live to the running container.
    await openTsEditor(page, 60000, projectId);
    await expectMemRow(page, LIMITED_512_RE, 'N / 512 MB', 30000);
    await closeLspTooltip(page);
    await closeExplorer(page);

    // 4. The saved value persists — reopening the form shows 512. Clearing the
    //    field (blank = no limit) goes back to unlimited, applied live.
    await openLspEditForm(page);
    await expect(page.locator(MEM_INPUT)).toHaveValue('512');
    await page.locator(MEM_INPUT).fill('');
    await saveEditForm(page);
    await closeSettings(page);

    // 5. Unlimited again.
    await openTsEditor(page, 60000, projectId);
    await expectMemRow(page, NO_LIMIT_RE, 'N MB (no limit)', 30000);
  });
});
