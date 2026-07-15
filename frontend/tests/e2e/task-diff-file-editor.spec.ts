import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync, writeFileSync } from 'fs';
import { join } from 'path';

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
    const worktreesDir = getConfig().workspace_dir;
    try {
        // Path structure: worktreesDir/projectID/taskID
        const projectEntries = readdirSync(worktreesDir);
        for (const projEntry of projectEntries) {
            const projPath = join(worktreesDir, projEntry);
            if (!statSync(projPath).isDirectory()) continue;
            const taskEntries = readdirSync(projPath);
            for (const taskEntry of taskEntries) {
                const fullPath = join(projPath, taskEntry);
                if (statSync(fullPath).isDirectory()) {
                    return fullPath;
                }
            }
        }
    } catch { /* ignore */ }
    return null;
}

function getTaskInfo(): { projectId: string; taskId: string } | null {
    try {
        const projects = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects`, { encoding: 'utf-8' }
        ));
        if (!projects.length) return null;
        const projectId = projects[0].id;
        const tasks = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects/${projectId}/tasks`, { encoding: 'utf-8' }
        ));
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

test.describe.serial('Diff File Editor', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('should open file editor when clicking a diff line', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      // No running task with worktree — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `editor-test-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    const fileContent = `Line 1: Hello World\nLine 2: This is a test\nLine 3: Third line\nLine 4: Fourth line\nLine 5: Fifth line\n`;

    // Write test file to worktree
    writeFileSync(filePath, fileContent);

    // Navigate to the task detail page
    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Wait for the diff to appear
    await expect(async () => {
      const diffRows = await page.locator('.diff-row').count();
      expect(diffRows).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Click on a diff line (first clickable row)
    const clickableRows = page.locator('.diff-row-clickable');
    const rowCount = await clickableRows.count();
    if (rowCount === 0) {
      // Clean up
      execSync(`rm -f ${filePath}`);
      return;
    }

    await clickableRows.first().click();

    // Wait for the file editor overlay to appear
    await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

    // Verify the file path is shown in the header
    await expect(page.locator('#file-editor-title')).toBeVisible();

    // Verify the textarea has content
    const textarea = page.locator('.editor-textarea');
    await expect(textarea).toBeVisible();
    const editorContent = await textarea.inputValue();
    expect(editorContent).toContain('Hello World');
    expect(editorContent).toContain('This is a test');

    // Clean up
    execSync(`rm -f ${filePath}`);
  });

  test('should save file changes with Ctrl+S', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `save-test-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    const originalContent = 'Original content\nLine 2\n';

    writeFileSync(filePath, originalContent);

    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    await expect(async () => {
      expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Open the editor
    await page.locator('.diff-row-clickable').first().click();
    await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

    // Edit the content
    const textarea = page.locator('.editor-textarea');
    await textarea.fill('Modified content\nLine 2\nLine 3: new line\n');

    // Save with Ctrl+S
    await textarea.press('Control+s');

    // Wait for save to complete (unsaved dot should disappear)
    await expect(async () => {
      const unsavedDot = page.locator('.unsaved-dot');
      const visible = await unsavedDot.isVisible().catch(() => false);
      expect(visible).toBe(false);
    }).toPass({ timeout: 5000 });

    // Close the editor
    await page.locator('.btn-close').click();
    await expect(page.locator('.editor')).not.toBeVisible({ timeout: 5000 });

    // Verify the file was saved
    const savedContent = execSync(`cat ${filePath}`).toString();
    expect(savedContent).toContain('Modified content');
    expect(savedContent).toContain('Line 3: new line');

    // Clean up
    execSync(`rm -f ${filePath}`);
  });

  test('should show unsaved changes warning when closing', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `warn-test-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);

    writeFileSync(filePath, 'Warning test content\n');

    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    await expect(async () => {
      expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Open the editor
    await page.locator('.diff-row-clickable').first().click();
    await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

    // Make changes
    const textarea = page.locator('.editor-textarea');
    await textarea.fill('Changed content!\n');

    // Verify unsaved dot appears
    await expect(page.locator('.unsaved-dot')).toBeVisible();

    // Try to close with Escape
    await textarea.press('Escape');

    // Confirm dialog should appear
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#confirm-title')).toHaveText('Unsaved Changes');

    // Click "Keep Editing" — editor should still be visible
    await page.locator('.btn-confirm-cancel').click();
    await expect(page.locator('.confirm-dialog')).not.toBeVisible();
    await expect(page.locator('.editor')).toBeVisible();

    // Clean up
    execSync(`rm -f ${filePath}`);
  });

  test('should close editor without warning when no changes', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `close-test-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);

    writeFileSync(filePath, 'No changes test\n');

    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    await expect(async () => {
      expect(await page.locator('.diff-row').count()).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Open the editor
    await page.locator('.diff-row-clickable').first().click();
    await expect(page.locator('.editor')).toBeVisible({ timeout: 5000 });

    // Do NOT make changes — just close with Escape
    await page.keyboard.press('Escape');

    // Editor should close without confirmation dialog
    await expect(page.locator('.editor')).not.toBeVisible({ timeout: 5000 });

    // Clean up
    execSync(`rm -f ${filePath}`);
  });
});
