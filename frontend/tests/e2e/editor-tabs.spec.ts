import { test, expect } from '@playwright/test';
import { writeFileSync, rmSync, readdirSync, statSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { getProjectId, isolatedProject, gotoProject, ownProjectPath, selectProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


test.describe.configure({ mode: 'parallel' });

// `ownProjectPath()` and `getProjectId()` name this worker's own project, from global setup. An empty one is
// the harness failing, not a condition a tab test can proceed without.

test.describe('Editor Tabs', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open multiple files as separate tabs', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-test-file1-${uniqueId}.txt`;
    const file2 = `tab-test-file2-${uniqueId}.txt`;

    writeFileSync(join(projectDir, file1), 'Content of file 1\nLine 2\nLine 3\n');
    writeFileSync(join(projectDir, file2), 'Content of file 2\nDifferent content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Pin the first file: a plain click opens a preview tab that the next click
      // replaces, and this test is about SEPARATE tabs.
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Verify first tab exists
      await expect(page.locator('.editor-tab')).toHaveCount(1);
      await expect(page.locator('.editor-tab-name')).toContainText(file1);

      // Open second file
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Both tab names should be visible
      const tabNames = page.locator('.editor-tab-name');
      await expect(tabNames).toHaveCount(2);

      // Second file should be active (last opened)
      const activeTab = page.locator('.editor-tab.active');
      await expect(activeTab.locator('.editor-tab-name')).toContainText(file2);

      // Click first tab to switch
      await tabNames.filter({ hasText: file1 }).click();

      // First file should now be active
      await expect(page.locator('.editor-tab.active .editor-tab-name')).toContainText(file1);

      // Verify CodeMirror shows file 1 content (active tab's editor is visible)
      await expect(page.locator('.cm-editor').first()).toBeVisible();
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should close a tab and switch to adjacent tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-close-file1-${uniqueId}.txt`;
    const file2 = `tab-close-file2-${uniqueId}.txt`;
    const file3 = `tab-close-file3-${uniqueId}.txt`;

    writeFileSync(join(projectDir, file1), 'File 1 content\n');
    writeFileSync(join(projectDir, file2), 'File 2 content\n');
    writeFileSync(join(projectDir, file3), 'File 3 content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Pin file1 and file2; file3 may stay a preview tab.
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file2 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file2 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file2 }).dblclick();
      await page.locator('.file-browser-item', { hasText: file3 }).click();

      await expect(page.locator('.editor-tab')).toHaveCount(3);

      // Close the middle tab (file2)
      const tabs = page.locator('.editor-tab');
      const middleTab = tabs.nth(1);
      await middleTab.locator('.btn-close-tab').click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // file2 should no longer be in tabs
      const tabNames = page.locator('.editor-tab-name');
      const names = await tabNames.allTextContents();
      expect(names).not.toContain(file2);
      expect(names).toContain(file1);
      expect(names).toContain(file3);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
        rmSync(join(projectDir, file3), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should close last tab and show placeholder', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-last-${uniqueId}.txt`;
    writeFileSync(join(projectDir, file1), 'Solo file content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the file
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.editor-tab')).toHaveCount(1);

      // Close the only tab
      await page.locator('.btn-close-tab').click();

      // Should show placeholder (no tabs, no editor)
      await expect(page.locator('.editor-tab')).toHaveCount(0);
      await expect(page.locator('.file-editor-placeholder')).toBeVisible();
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should show unsaved indicator on tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-unsaved-${uniqueId}.txt`;
    writeFileSync(join(projectDir, file1), 'Original content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the file
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Tab should NOT have unsaved class initially
      const tab = page.locator('.editor-tab');
      expect(await tab.first().getAttribute('class')).not.toContain('unsaved');

      // Edit the file in CodeMirror — click into it first, it is not autofocused
      await page.locator('.cm-content').click();
      await page.keyboard.press('End');
      await page.keyboard.type(' Modified');

      // Wait for the change to be detected
      await page.waitForTimeout(300);

      // Tab should now have unsaved indicator
      await expect(tab.first()).toHaveClass(/unsaved/);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should re-open an already-open file and switch to its tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-reopen-${uniqueId}a.txt`;
    const file2 = `tab-reopen-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'File 1\n');
    writeFileSync(join(projectDir, file2), 'File 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 pinned, so file2 gets its own tab
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.editor-tab-name').filter({ hasText: file1 })).toBeVisible({ timeout: 10000 });
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Open file2
      await page.locator('.file-browser-item', { hasText: file2 }).click();
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Click file1 again in the file browser: it is already open, so this must
      // switch to its tab rather than open a third one.
      await page.locator('.file-browser-item', { hasText: file1 }).click();

      // Should still have 2 tabs (no duplicate)
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // file1 should be active
      await expect(page.locator('.editor-tab.active .editor-tab-name')).toContainText(file1);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should open new files in preview mode (italicized) and replace preview tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-preview-${uniqueId}a.txt`;
    const file2 = `tab-preview-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'Preview file 1\n');
    writeFileSync(join(projectDir, file2), 'Preview file 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 (single click = preview mode)
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Tab should be in preview mode (italicized)
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file1);

      // Open file2 (single click = replaces preview tab)
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should still have only 1 tab (preview was replaced)
      await expect(page.locator('.editor-tab')).toHaveCount(1);

      // Tab should show file2 in preview mode
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file2);

      // file1 tab should be gone
      const tabNames = await page.locator('.editor-tab-name').allTextContents();
      expect(tabNames).not.toContain(file1);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should promote preview tab to pinned on double-click', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-pin-${uniqueId}a.txt`;
    const file2 = `tab-pin-${uniqueId}b.txt`;

    writeFileSync(join(projectDir, file1), 'Pin file 1\n');
    writeFileSync(join(projectDir, file2), 'Pin file 2\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open file1 in preview mode (single click)
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Tab should be in preview mode
      await expect(page.locator('.editor-tab-name.preview')).toContainText(file1);

      // Double-click the file in the file browser to pin it
      await page.locator('.file-browser-item', { hasText: file1 }).dblclick();

      // Tab should no longer be in preview mode (promoted to pinned)
      await expect(page.locator('.editor-tab-name.preview')).not.toBeVisible({ timeout: 2000 });
      await expect(page.locator('.editor-tab-name')).toContainText(file1);

      // Open file2 — should create a NEW tab (not replace the pinned one)
      await page.locator('.file-browser-item', { hasText: file2 }).click();

      // Should now have 2 tabs
      await expect(page.locator('.editor-tab')).toHaveCount(2);

      // Both files should be present
      const tabNames = await page.locator('.editor-tab-name').allTextContents();
      expect(tabNames).toContain(file1);
      expect(tabNames).toContain(file2);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
        rmSync(join(projectDir, file2), { force: true });
      } catch { /* ignore */ }
    }
  });

  test('should warn about unsaved changes when closing a tab', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer lists the project directory, so that is where the fixture
    // files have to be written — not a task worktree.
    const projectDir = ownProjectPath();
    expect(projectDir, 'this worker has no own project path').toBeTruthy();
    expect(projectId, 'this worker has no own project id').toBeTruthy();

    const uniqueId = Date.now();
    const file1 = `tab-unsaved-close-${uniqueId}.txt`;
    writeFileSync(join(projectDir, file1), 'Original content\n');

    try {
      // Open explorer
      await selectProject(page, projectId);
      await page.waitForSelector('.top-bar', { timeout: 10000 });
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
      await expect(page.locator('.file-browser-list')).toBeVisible({ timeout: 10000 });

      // Open the file
      await page.locator('.file-browser-item', { hasText: file1 }).click();
      await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 10000 });

      // Edit the file to create unsaved changes (focus the editor first)
      await page.locator('.cm-content').click();
      await page.keyboard.press('End');
      await page.keyboard.type(' Modified');
      await page.waitForTimeout(300);

      // Tab should have unsaved indicator
      await expect(page.locator('.editor-tab.unsaved')).toBeVisible();

      // Try to close the tab
      await page.locator('.btn-close-tab').click();

      // Confirmation dialog should appear
      await expect(page.locator('.overlay')).toBeVisible({ timeout: 5000 });
      await expect(page.locator('.modal')).toContainText('Unsaved changes');
      await expect(page.locator('.modal')).toContainText('Discard');

      // Cancel — tab should stay open (ConfirmationDialog renders .btn-cancel/.btn-confirm)
      await page.locator('.modal-footer .btn-cancel').click();
      await expect(page.locator('.overlay')).not.toBeVisible({ timeout: 5000 });
      await expect(page.locator('.editor-tab')).toHaveCount(1);

      // Try closing again, this time confirm
      await page.locator('.btn-close-tab').click();
      await expect(page.locator('.overlay')).toBeVisible({ timeout: 5000 });
      await page.locator('.btn-confirm').click();

      // Tab should be closed
      await expect(page.locator('.overlay')).not.toBeVisible({ timeout: 5000 });
      await expect(page.locator('.editor-tab')).toHaveCount(0);
    } finally {
      try {
        rmSync(join(projectDir, file1), { force: true });
      } catch { /* ignore */ }
    }
  });
});
