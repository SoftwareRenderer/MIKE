import { test, expect, type Page } from '@playwright/test';
import { writeFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { getProjectId, projectDirPath, selectProject, isolatedProject, gotoProject } from './helpers';

// ============================================================
// Editor reconciliation of externally-changed files
// ============================================================
//
// Split out of file-editor.spec.ts: these write into the PROJECT directory and wait for the open
// editor to notice, and that file's Explorer tests stage/reset files in the same repo.

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe('Editor External Change Reconciliation', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openFileInEditor(page: Page, projectId: string, projectDir: string, file: string) {
    // Select by id: the sidebar's first row is not necessarily this project's row.
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 10000 });
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });
    await page.locator('.file-browser-item', { hasText: file }).click();
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
  }

  test('reloads an open file when it is changed externally with no unsaved edits', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer and Review-Changes read the PROJECT directory, not a task
    // worktree — see projectDirPath() in helpers.ts.
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-reload-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);
      await expect(page.locator('.cm-content')).toContainText('Line 2: beta');

      // Change the file externally (editor has no unsaved edits)
      writeFileSync(absFile, 'Line 1: alpha\nLine 2: BETA-CHANGED\nLine 3: gamma\n');

      // The reload is driven by the editor's external-change poll, whose interval is
      // long relative to a single test; under parallel load it regularly needs more
      // than the 10s default, so ask for 25s explicitly.
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }, { timeout: 25000 }).toContain('BETA-CHANGED');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });

  test('merges an external change with non-overlapping unsaved edits', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer and Review-Changes read the PROJECT directory, not a task
    // worktree — see projectDirPath() in helpers.ts.
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-merge-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);

      // User edits line 1 in the editor. Clicking .cm-content puts the caret on
      // whichever line its centre lands on, so the line has to be selected explicitly —
      // otherwise "non-overlapping" isn't, and the merge is being tested by luck.
      await page.locator('.cm-content').first().click();
      await page.keyboard.press('Control+Home'); // document start
      await page.keyboard.press('End');          // end of line 1
      await page.keyboard.type(' USER');
      await page.waitForTimeout(400); // let draft/unsaved state settle

      // Externally edit a different line (line 3)
      writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma EXTERNAL\n');

      // Both the user edit AND the external change must be present
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }).toContain('alpha USER');
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }).toContain('gamma EXTERNAL');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });

  test('keeps the user buffer and shows a toast on a conflicting external change', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer and Review-Changes read the PROJECT directory, not a task
    // worktree — see projectDirPath() in helpers.ts.
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-conflict-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);

      // User edits line 1 — the same line the external change will touch, which is
      // only guaranteed if the caret is placed explicitly (a click lands on whichever
      // line its centre hits).
      await page.locator('.cm-content').first().click();
      await page.keyboard.press('Control+Home');
      await page.keyboard.press('End');
      await page.keyboard.type(' USER');
      await page.waitForTimeout(400);

      // Externally edit the SAME line differently
      writeFileSync(absFile, 'Line 1: alpha EXTERNAL\nLine 2: beta\nLine 3: gamma\n');

      // User edit must be preserved (not overwritten by the external change)
      await page.waitForTimeout(1200); // allow debounce + fetch + reconcile
      const content = (await page.locator('.cm-content').textContent()) ?? '';
      expect(content).toContain('alpha USER');
      expect(content).not.toContain('alpha EXTERNAL');

      // A non-blocking toast should be shown
      await expect(page.locator('.toast')).toBeVisible({ timeout: 5000 });
      await expect(page.locator('.toast')).toContainText('changed externally');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });
});
