import { test, expect, type Page } from '@playwright/test';
import { writeFileSync, rmSync } from 'node:fs';
import { join } from 'path';
import { getProjectId, projectDirPath, selectProject, isolatedProject, gotoProject } from './helpers';

// ============================================================
// Editor reconciliation of externally-changed files
// ============================================================
//
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

  /** Type at the end of line 1. A click lands the caret on whichever line its centre hits, so the line an
   *  edit belongs on has to be selected explicitly. */
  async function editEndOfLine1(page: Page, text: string) {
    await page.locator('.cm-content').first().click();
    await page.keyboard.press('Control+Home');
    await page.keyboard.press('End');
    await page.keyboard.type(text);
  }

  test('reloads an open file when it is changed externally with no unsaved edits', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer reads the PROJECT directory, not a task worktree — see projectDirPath().
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-reload-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);
      await expect(page.locator('.cm-content')).toContainText('Line 2: beta');

      writeFileSync(absFile, 'Line 1: alpha\nLine 2: BETA-CHANGED\nLine 3: gamma\n');

      // The editor's external-change poll is slow relative to one test, so this wait is explicit.
      await expect.poll(async () => {
        return (await page.locator('.cm-content').textContent()) ?? '';
      }, { timeout: 25000 }).toContain('BETA-CHANGED');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });

  test('merges an external change with non-overlapping unsaved edits', async ({ page }) => {
    const projectId = await getProjectId(page);
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-merge-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);

      await editEndOfLine1(page, ' USER');
      await page.waitForTimeout(400); // let draft/unsaved state settle

      writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma EXTERNAL\n');

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

  test('writes both versions into the buffer on a conflicting external change', async ({ page }) => {
    const projectId = await getProjectId(page);
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const file = `ext-conflict-${uniqueId}.txt`;
    const absFile = join(projectDir, file);
    writeFileSync(absFile, 'Line 1: alpha\nLine 2: beta\nLine 3: gamma\n');

    try {
      await openFileInEditor(page, projectId, projectDir, file);

      // The same line the external change is about to move.
      await editEndOfLine1(page, ' USER');
      await page.waitForTimeout(400);

      writeFileSync(absFile, 'Line 1: alpha EXTERNAL\nLine 2: beta\nLine 3: gamma\n');

      // Neither side is dropped: the buffer carries both, between the markers, for the reader to resolve.
      // Keeping only its own buffer would write the external change away on the next save.
      await expect(page.locator('.conflict-banner')).toBeVisible({ timeout: 25000 });
      const content = (await page.locator('.cm-content').textContent()) ?? '';
      expect(content).toContain('alpha USER');
      expect(content).toContain('alpha EXTERNAL');
      expect(content).toContain('unsaved');

      // And the reader is told, because the file on disk now disagrees with what was opened.
      await expect(page.locator('.toast')).toBeVisible({ timeout: 5000 });
      await expect(page.locator('.toast')).toContainText('changed on disk');
    } finally {
      try { rmSync(absFile, { force: true }); } catch { /* ignore */ }
    }
  });
});
