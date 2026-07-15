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

function getProjectId(): string | null {
    try {
        const projects = JSON.parse(execSync(
            'curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }
        ));
        return projects.length > 0 ? projects[0].id : null;
    } catch {
        return null;
    }
}

test.describe.serial('Explorer Diff Line Click', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open editor at clicked line from Review Changes diff', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = getProjectId();
    if (!worktreeDir || !projectId) {
      // No worktree or project — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `diff-line-click-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    // Create a file with enough lines to pick a specific target
    const fileContent = Array.from({ length: 20 }, (_, i) => `Line ${i + 1}: content here`).join('\n') + '\n';
    writeFileSync(filePath, fileContent);

    // Click on the project to select it (if not already selected)
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Click the "Review Changes" button (git-status-btn in explorer header)
    await page.locator('.git-status-btn').click();

    // Wait for the Changes panel header to appear
    await expect(page.locator('.panel-title')).toHaveText('Changes', { timeout: 10000 });

    // Wait for diff rows to appear
    await expect(async () => {
      const diffRows = await page.locator('.diff-row').count();
      expect(diffRows).toBeGreaterThan(0);
    }).toPass({ timeout: 10000 });

    // Find a clickable diff row with a line number
    const clickableRows = page.locator('.diff-row-clickable');
    const rowCount = await clickableRows.count();
    if (rowCount === 0) {
      // Clean up
      execSync(`rm -f ${filePath}`);
      return;
    }

    // Pick a row that has a visible line number (not the first one which might be a header)
    // Get the line number from the gutter next to the clickable row
    // We'll click on a row and verify the editor opens
    const targetRow = clickableRows.nth(Math.min(5, rowCount - 1)); // pick ~line 6 area
    await targetRow.click();

    // The Review Changes panel should close and the editor should open
    // Wait for the editor to appear (branch-editor)
    await expect(page.locator('.branch-editor')).toBeVisible({ timeout: 10000 });

    // Verify the file is loaded (loading state disappears)
    await expect(page.locator('.editor-placeholder:has-text("Loading file")')).not.toBeVisible({ timeout: 10000 });

    // Verify the file name is shown in the editor header
    await expect(page.locator('.editor-filename')).toContainText(testFile);

    // Verify CodeMirror editor is present
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 5000 });

    // Verify the cursor is on a specific line (not line 1)
    // The active line gutter should show a line number > 1
    const activeLineGutter = page.locator('.cm-activeLineGutter');
    await expect(activeLineGutter).toBeVisible({ timeout: 5000 });
    const activeLineNumber = await activeLineGutter.textContent();
    expect(activeLineNumber).toBeTruthy();
    const lineNum = parseInt(activeLineNumber!.trim(), 10);
    // Should be on a line > 1 (we clicked on a row ~5 lines in)
    expect(lineNum).toBeGreaterThan(1);

    // Clean up
    execSync(`rm -f ${filePath}`);
  });
});
