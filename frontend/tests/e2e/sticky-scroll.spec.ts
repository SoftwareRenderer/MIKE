import { test, expect } from '@playwright/test';
import { writeFileSync, rmSync, mkdirSync, readdirSync, statSync, existsSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';

/**
 * sticky-scroll.spec.ts
 *
 * Tests the CodeMirror "sticky scroll" bar that shows the current section
 * (e.g., function) header while scrolling.
 *
 * Validates three fixes:
 *   1. The bar updates when a header line is renamed in place.
 *   2. Headers are re-extracted after external file changes (disk sync).
 *   3. Symbol extraction works using Lezer fallback when no LSP is available.
 *
 * Fixture Details:
 *   - Uses a `.js` file to test Lezer extraction (no JS LSP in test env).
 *   - Sections are long because the sticky bar only appears if the section
 *     body extends beyond the top of the viewport.
 *
 * Isolation:
 *   - Uses a dedicated repo (/tmp/kanban-test-repo-stickyscroll) to avoid
 *     conflicts with other tests.
 */

// Dedicated repo to avoid races with other tests.
const STICKY_REPO = '/tmp/kanban-test-repo-stickyscroll';
const PROJECT_NAME = 'sticky-scroll-e2e';

// Main test server (this spec runs in the default chromium project).
const MAIN_BASE = 'http://localhost:3000';

/**
 * Finds the active task worktree directory.
 */
function findWorktreeDir(workspaceDir: string, projectId: string, taskId: string): string | null {
  if (!workspaceDir) return null;
  const projectDir = join(workspaceDir, projectId);
  try {
    if (!statSync(projectDir).isDirectory()) return null;
    const taskEntries = readdirSync(projectDir);
    // Prefer the exact task; fall back to the first task worktree in the
    // project (mirrors the original helper's "first directory" behaviour).
    const preferred = join(projectDir, taskId);
    if (taskId && statSync(preferred).isDirectory()) return preferred;
    for (const taskEntry of taskEntries) {
      const fullPath = join(projectDir, taskEntry);
      if (statSync(fullPath).isDirectory()) return fullPath;
    }
  } catch {
    /* ignore */
  }
  return null;
}

// Fixture with 3 functions. The middle one is long to ensure
// the sticky bar appears when scrolling through its body.
function makeFixture(): string {
  const lines: string[] = [];
  lines.push('// sticky-scroll e2e fixture'); // 1
  lines.push(''); // 2
  lines.push('function alphaSectionOne() {'); // 3
  for (let i = 1; i <= 12; i++) lines.push(`  const v${i} = ${i};`); // 4..15
  lines.push('}'); // 16
  lines.push(''); // 17
  lines.push('// --- section two ---'); // 18
  lines.push(''); // 19
  lines.push('function betaSectionTwo() {'); // 20
  for (let i = 1; i <= 45; i++) lines.push(`  const w${i} = ${i};`); // 21..65
  lines.push('}'); // 66
  lines.push(''); // 67
  lines.push('// --- section three ---'); // 68
  lines.push(''); // 69
  lines.push('function gammaSectionThree() {'); // 70
  for (let i = 1; i <= 12; i++) lines.push(`  const x${i} = ${i};`); // 71..82
  lines.push('}'); // 83
  lines.push(''); // 84
  lines.push('// end of fixture'); // 85
  return lines.join('\n') + '\n';
}

const BETA_NAME = 'betaSectionTwo';
// Declaration line of the middle function (1-based) in the original fixture.
const BETA_DECL_LINE = 20;
// Scroll ~32 lines below beta's start to ensure it's the active section
// in the sticky bar (accounting for CodeMirror's viewport offset).
const BETA_SCROLL_LINE = BETA_DECL_LINE + 32;

/**
 * Scrolls the editor to a specific line using CodeMirror internals.
 */
async function scrollEditorToLine(page: any, line: number): Promise<void> {
  await page.evaluate((n: number) => {
    const v = (document.querySelector('.cm-editor') as any)?.__view;
    if (!v) return;
    const pos = v.state.doc.line(n).from;
    v.dispatch({ selection: { anchor: pos, head: pos } });
    // Scroll the line to the top of the visible area.
    v.dispatch({ effects: v.constructor.scrollIntoView(pos, { y: 'start', yMargin: 0 }) });
  }, line);
}

/** Gets the current sticky bar text. */
async function stickyBarText(page: any): Promise<string> {
  return await page.evaluate(() => {
    const bar = document.querySelector('.cm-sticky-scroll') as HTMLElement | null;
    if (!bar) return '';
    if (bar.style.display === 'none') return '';
    return bar.textContent || '';
  });
}

/** Opens a file in the editor using the task explorer. */
async function openFileInEditor(page: any, worktreeDir: string, projectId: string, taskId: string, file: string): Promise<void> {
  void worktreeDir; // fixture already written into the worktree by the caller
  // Select the dedicated project, then open its task.
  await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
  await page.waitForSelector('.board', { timeout: 10000 });
  // The dedicated project has exactly one task — the first card is ours.
  await page.locator('.card').first().click();
  await page.waitForSelector('.task-detail-page', { timeout: 10000 });
  // Open the task explorer (rooted at the worktree).
  await page.locator('.task-detail-page').getByRole('button', { name: 'Open explorer' }).click();
  await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
  await expect(page.locator('.file-list')).toBeVisible({ timeout: 10000 });
  await page.locator('.file-entry', { hasText: file }).click();
  await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
  void projectId;
  void taskId;
}

let workspaceDir = '';
let projectId = '';
let taskId = '';
let worktreeDir = '';
let csrfToken = '';

/**
 * Helper for authenticated API calls (attaches CSRF token).
 */
async function apiMutate(request: any, method: 'POST' | 'PUT' | 'DELETE', url: string, data?: unknown) {
  const headers: Record<string, string> = {};
  if (csrfToken) headers['X-CSRF-Token'] = csrfToken;
  return await request.fetch(url, {
    method,
    headers,
    data: data === undefined ? undefined : JSON.parse(JSON.stringify(data)),
  });
}

// serial: tests share the dedicated project + worktree created in beforeAll.
test.describe.serial('CodeMirror sticky scroll section header', () => {
  test.beforeAll(async ({ request }) => {
    // Setup: create dedicated repo, project, and task.
    const cfgRes = await request.get('/api/config');
    expect(cfgRes.ok()).toBeTruthy();
    const cfg = await cfgRes.json();
    workspaceDir = cfg.workspace_dir || '';
    const workerProvider = (cfg.providers || []).find(
      (p: any) => p.name === cfg.worker_provider,
    );
    const llmUrl = workerProvider?.url || '';
    const llmModel = cfg.worker_model || '';

    // CSRF token for the mutating calls below.
    const sessionRes = await request.get('/api/auth/session');
    const session = await sessionRes.json().catch(() => ({}));
    csrfToken = session.csrf_token || session.csrfToken || '';

// 2. Create dedicated git repo.
    try {
      rmSync(STICKY_REPO, { recursive: true, force: true });
    } catch {
      /* ignore */
    }
    mkdirSync(STICKY_REPO, { recursive: true });
    writeFileSync(join(STICKY_REPO, 'README.md'), 'sticky-scroll fixture repo\n');
    execSync('git init -q', { cwd: STICKY_REPO, stdio: 'ignore' });
    execSync('git config user.email "test@kanban.local"', { cwd: STICKY_REPO, stdio: 'ignore' });
    execSync('git config user.name "Test"', { cwd: STICKY_REPO, stdio: 'ignore' });
    execSync('git add README.md', { cwd: STICKY_REPO, stdio: 'ignore' });
    execSync('git commit -qm "Initial commit"', { cwd: STICKY_REPO, stdio: 'ignore' });

// 3. Create project.
    const projRes = await apiMutate(request, 'POST', '/api/projects', {
      name: PROJECT_NAME,
      path: STICKY_REPO,
    });
    expect(projRes.ok(), `create project: ${await projRes.text()}`).toBeTruthy();
    const proj = await projRes.json();
    projectId = proj.id;

// 4. Create and start task to materialize worktree.
    const taskRes = await apiMutate(request, 'POST', `/api/projects/${projectId}/tasks`, {
      prompt: 'Sticky scroll e2e fixture task',
      llama_server_url: llmUrl,
      model_name: llmModel,
    });
    expect(taskRes.ok(), `create task: ${await taskRes.text()}`).toBeTruthy();
    const task = await taskRes.json();
    taskId = task.id;

    const moveRes = await apiMutate(request, 'POST', `/api/projects/${projectId}/tasks/${taskId}/move`, {
      status: 'in_progress',
    });
    expect(moveRes.ok(), `start task: ${await moveRes.text()}`).toBeTruthy();

// 5. Wait for worktree directory.
    const deadline = Date.now() + 30000;
    while (Date.now() < deadline) {
      worktreeDir = findWorktreeDir(workspaceDir, projectId, taskId) || '';
      if (worktreeDir && existsSync(worktreeDir)) break;
      await new Promise((r) => setTimeout(r, 500));
    }
    expect(worktreeDir, `worktree for task ${taskId} not found under ${workspaceDir}`).toBeTruthy();
  });

  test.afterAll(async ({ request }) => {
// Cleanup project and repo.
    if (projectId) {
      await apiMutate(request, 'DELETE', `/api/projects/${projectId}`).catch(() => {});
    }
    try {
      rmSync(STICKY_REPO, { recursive: true, force: true });
    } catch {
      /* ignore */
    }
  });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('shows the middle section header while scrolling inside it (Lezer fallback, no LSP)', async ({ page }) => {
    const uniqueId = Date.now();
    const file = `sticky-base-${uniqueId}.js`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, makeFixture());

    try {
      await openFileInEditor(page, worktreeDir, projectId, taskId, file);

      // Scroll into beta's body. The sticky bar should show its header.
      // This also verifies Lezer fallback extraction (no LSP for .js).
      await scrollEditorToLine(page, BETA_SCROLL_LINE);
      await expect.poll(() => stickyBarText(page), { timeout: 10000 }).toContain(BETA_NAME);

      // And the bar is actually visible (not display:none).
      await expect(async () => {
        const visible = await page.evaluate(() => {
          const bar = document.querySelector('.cm-sticky-scroll') as HTMLElement | null;
          return !!bar && bar.style.display !== 'none';
        });
        expect(visible).toBeTruthy();
      }).toPass({ timeout: 5000 });
    } finally {
      try {
        rmSync(absFile, { force: true });
      } catch {
        /* ignore */
      }
    }
  });

  test('re-renders the bar when the header line is renamed in place', async ({ page }) => {
    const uniqueId = Date.now();
    const file = `sticky-rename-${uniqueId}.js`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, makeFixture());

    try {
      await openFileInEditor(page, worktreeDir, projectId, taskId, file);

      // Baseline: bar shows the original beta header.
      await scrollEditorToLine(page, BETA_SCROLL_LINE);
      await expect.poll(() => stickyBarText(page), { timeout: 10000 }).toContain(BETA_NAME);

      // Rename the function in place. Scroll to line, select all, and type new name.
      const NEW_NAME = 'betaSectionTwoRenamed';
      await scrollEditorToLine(page, BETA_DECL_LINE);
      const betaDeclLine = page.locator('.cm-line', { hasText: `function ${BETA_NAME}` }).first();
      await expect(betaDeclLine).toBeVisible({ timeout: 5000 });
      await betaDeclLine.click();
      // Sanity: the caret is now on the declaration line.
      await expect(async () => {
        const caretLine = await page.evaluate(() => {
          const v = (document.querySelector('.cm-editor') as any)?.__view;
          if (!v) return -1;
          return v.state.doc.lineAt(v.state.selection.main.head).number;
        });
        expect(caretLine).toBe(BETA_DECL_LINE);
      }).toPass({ timeout: 5000 });
      await page.keyboard.press('Home');
      await page.keyboard.press('End', { modifiers: ['Shift'] });
      await page.keyboard.type(`function ${NEW_NAME}() {`);

      // The declaration line must actually have been renamed in the doc.
      await expect(page.locator('.cm-line', { hasText: `function ${NEW_NAME}` })).toBeVisible({ timeout: 5000 });

      // Scroll back into beta's body; the bar should now show the new name.
      await scrollEditorToLine(page, BETA_SCROLL_LINE);
      await expect.poll(() => stickyBarText(page), { timeout: 10000 }).toContain(NEW_NAME);
    } finally {
      try {
        rmSync(absFile, { force: true });
      } catch {
        /* ignore */
      }
    }
  });

  test('re-extracts headers after an external file change (rename + prepended lines)', async ({ page }) => {
    const uniqueId = Date.now();
    const file = `sticky-ext-${uniqueId}.js`;
    const absFile = join(worktreeDir, file);
    writeFileSync(absFile, makeFixture());

    try {
      await openFileInEditor(page, worktreeDir, projectId, taskId, file);

      // Baseline: verify beta is in the symbol outline at the correct line.
      const betaItem = page
        .locator('.symbol-item', { has: page.locator('.symbol-name', { hasText: new RegExp(`^${BETA_NAME}$`) }) })
        .first();
      await expect(betaItem).toBeVisible({ timeout: 10000 });
      await expect(betaItem.locator('.symbol-line')).toHaveText(String(BETA_DECL_LINE));

      // Externally rewrite the file: rename beta and prepend 10 lines.
      const NEW_NAME = 'betaSectionTwoExternal';
      const original = makeFixture();
      const renamed = original.replace(`function ${BETA_NAME}() {`, `function ${NEW_NAME}() {`);
      const prepended =
        Array.from({ length: 10 }, (_, i) => `// externally prepended ${i + 1}`).join('\n') +
        '\n' +
        renamed;
      writeFileSync(absFile, prepended);

      // Wait for the editor to sync external changes.
      await expect
        .poll(async () => (await page.locator('.cm-content').textContent()) ?? '')
        .toContain(NEW_NAME);

      // Use the symbol outline to verify the fix. A refreshed outline proves
      // that symbols were re-extracted. We avoid asserting on the visible bar
      // because viewport tracking often freezes in headless mode after a full
      // doc replace (see diag-sticky-external.mjs).
      //
      // We make no selection changes here to ensure the re-extraction is triggered
      // by the disk sync, not by the selection-change debounce.
      const newBetaItem = page
        .locator('.symbol-item', { has: page.locator('.symbol-name', { hasText: new RegExp(`^${NEW_NAME}$`) }) })
        .first();
      await expect(newBetaItem).toBeVisible({ timeout: 10000 });
      // Declaration shifted down by the 10 prepended lines.
      await expect(newBetaItem.locator('.symbol-line')).toHaveText(String(BETA_DECL_LINE + 10));
      // The old (stale) entry is gone.
      await expect(
        page.locator('.symbol-item', { has: page.locator('.symbol-name', { hasText: new RegExp(`^${BETA_NAME}$`) }) }),
      ).toHaveCount(0);
    } finally {
      try {
        rmSync(absFile, { force: true });
      } catch {
        /* ignore */
      }
    }
  });
});
