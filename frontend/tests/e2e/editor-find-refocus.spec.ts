import { test, expect, type Page } from '@playwright/test';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch } from './helpers';

// ============================================================
// EDITOR FIND REFOCUS
// ============================================================
// In the Editor, when Find is open and the user moves focus into the code
// editor, pressing Ctrl+F must refocus the find input (previously the keymap
// handler no-oped because the panel was already mounted).
//
// Uses a dedicated repo + project (like board-arrow-nav.spec.ts): the shared
// /tmp/kanban-test-repo is mutated by other specs in parallel.
test.describe.serial('Editor Find Refocus', () => {
  const REPO = '/tmp/kanban-test-repo-find-refocus';
  const PROJECT_NAME = 'find-refocus-project';
  let projectId: string | null = null;

  test.beforeAll(async ({ browser }) => {
    // `page` is not available in beforeAll — create a throwaway context.
    try { rmSync(REPO, { recursive: true, force: true }); } catch { /* ignore */ }
    mkdirSync(REPO, { recursive: true });
    writeFileSync(join(REPO, 'README.md'), '# Find Refocus Fixture\n');
    execSync('git init', { cwd: REPO });
    execSync('git config user.email "test@kanban.local"', { cwd: REPO });
    execSync('git config user.name "Test"', { cwd: REPO });
    execSync('git add -A && git commit -m "init"', { cwd: REPO });

    const ctx = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
    const page = await ctx.newPage();
    const res = await csrfFetch(page, '/api/projects', {
      method: 'POST',
      body: { name: PROJECT_NAME, path: REPO },
    });
    await ctx.close();
    if (!res.ok) throw new Error(`create project: ${JSON.stringify(res.data)}`);
    projectId = res.data.id;
  });

  test.afterAll(async ({ browser }) => {
    const ctx = await browser.newContext({ storageState: '/tmp/kanban-test-storage-state.json' });
    const page = await ctx.newPage();
    if (projectId) await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
    await ctx.close();
    try { rmSync(REPO, { recursive: true, force: true }); } catch { /* ignore */ }
  });

  test.beforeEach(() => {
    test.setTimeout(30000);
  });

  /**
   * Write the fixture file, then select the project and open it in the
   * editor via the Explorer file browser (the browser must load after the
   * file exists so the listing includes it).
   */
  async function openFileInEditor(page: Page, file: string, content: string) {
    writeFileSync(join(REPO, file), content);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item', { hasText: PROJECT_NAME }).click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    await page.locator('.file-browser-item', { hasText: file }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
  }

  const findInput = (page: Page) => page.locator('.search-container input[placeholder="Find"]');

  test('Ctrl+F refocuses the find input when focus is in the code editor', async ({ page }) => {
    const file = `refocus-${Date.now()}.txt`;
    try {
      await openFileInEditor(page, file, 'Line 1: alpha\nLine 2: beta\nLine 3: alpha again\n');
      await expect(page.locator('.cm-content')).toContainText('Line 2: beta');

      // Focus the editor (the Ctrl+F keymap lives on the CodeMirror view)
      await page.locator('.cm-content').click();
      await expect(page.locator('.cm-content')).toBeFocused();

      // 1. Open the find panel — it focuses the find input
      await page.keyboard.press('Control+f');
      await expect(findInput(page)).toBeVisible({ timeout: 5000 });
      await expect(findInput(page)).toBeFocused();

      // 2. Move focus into the code editor
      await page.locator('.cm-content').click();
      await expect(page.locator('.cm-content')).toBeFocused();

      // 3. Ctrl+F again — refocuses the find input
      await page.keyboard.press('Control+f');
      await expect(findInput(page)).toBeFocused();
    } finally {
      try { rmSync(join(REPO, file), { force: true }); } catch { /* ignore */ }
    }
  });

  test('Ctrl+F from the code editor refocuses the find input (not replace) with the replace row open', async ({ page }) => {
    const file = `refocus-replace-${Date.now()}.txt`;
    try {
      await openFileInEditor(page, file, 'needle one\nneedle two\n');
      await expect(page.locator('.cm-content')).toContainText('needle one');

      // Focus the editor (the Ctrl+F keymap lives on the CodeMirror view)
      await page.locator('.cm-content').click();
      await expect(page.locator('.cm-content')).toBeFocused();

      // Open find, type a query, open the replace row
      await page.keyboard.press('Control+f');
      await expect(findInput(page)).toBeFocused();
      await page.keyboard.type('needle');
      await expect(findInput(page)).toHaveValue('needle');

      await page.locator('.search-container .toggle-replace-btn').click();
      const replaceInput = page.locator('.search-container input.replace-input');
      await expect(replaceInput).toBeVisible({ timeout: 5000 });
      await replaceInput.fill('haystack');

      // Move focus into the code editor, then Ctrl+F
      await page.locator('.cm-content').click();
      await expect(page.locator('.cm-content')).toBeFocused();
      await page.keyboard.press('Control+f');

      // Focus lands on the find input — not the replace input
      await expect(findInput(page)).toBeFocused();
    } finally {
      try { rmSync(join(REPO, file), { force: true }); } catch { /* ignore */ }
    }
  });

  test('Escape still closes the find panel and returns focus to the editor', async ({ page }) => {
    const file = `refocus-escape-${Date.now()}.txt`;
    try {
      await openFileInEditor(page, file, 'escape test content\n');
      await expect(page.locator('.cm-content')).toContainText('escape test content');

      // Focus the editor (the Ctrl+F keymap lives on the CodeMirror view)
      await page.locator('.cm-content').click();
      await expect(page.locator('.cm-content')).toBeFocused();

      // Open the panel
      await page.keyboard.press('Control+f');
      await expect(findInput(page)).toBeFocused();

      // Escape closes the panel and returns focus to the editor
      await page.keyboard.press('Escape');
      await expect(findInput(page)).toBeHidden({ timeout: 5000 });
      await expect(page.locator('.cm-content')).toBeFocused();

      // Ctrl+F opens it again (fresh open still works after close)
      await page.keyboard.press('Control+f');
      await expect(findInput(page)).toBeFocused();
    } finally {
      try { rmSync(join(REPO, file), { force: true }); } catch { /* ignore */ }
    }
  });
});
