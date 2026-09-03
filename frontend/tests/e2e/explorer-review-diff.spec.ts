import { test, expect } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { getProjectId, projectDirPath, selectProject, isolatedProject, gotoProject } from './helpers';

// ============================================================
// Explorer: Review Changes diff -> editor at the clicked line
// ============================================================
//
// Own project: this test stages a file in the project directory, and other specs run
// `git add -A && git commit` in the shared one — concurrent index writes in one repo.

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

test.describe('Explorer Review Changes', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open editor at clicked line from Review Changes diff', async ({ page }) => {
    const projectId = await getProjectId(page);
    // The project explorer and Review-Changes read the PROJECT directory, not a task
    // worktree — see projectDirPath() in helpers.ts.
    const projectDir = await projectDirPath(page, projectId);

    const uniqueId = Date.now();
    const testFile = `diff-line-click-${uniqueId}.txt`;
    const filePath = join(projectDir, testFile);
    // Create a file with enough lines to pick a specific target
    const fileContent = Array.from({ length: 20 }, (_, i) => `Line ${i + 1}: content here`).join('\n') + '\n';
    writeFileSync(filePath, fileContent);
    // Stage it: an UNTRACKED file is listed under "Untracked" with no line-by-line diff
    // (it only shows a per-file preview once expanded), so clicking a diff line needs a
    // staged change.
    execSync(`git -C ${JSON.stringify(projectDir)} add -- ${JSON.stringify(testFile)}`);

    // Select the project under test. `.project-item` + `.first()` is the first row of
    // the sidebar, which once per-worker projects exist is some other test's project —
    // the folder then gets created somewhere else and this test reads its own dir.
    await selectProject(page, projectId);
    await page.waitForSelector('.top-bar', { timeout: 10000 });

    // Open the explorer view by clicking the project title button
    await page.locator('.explorer-btn').click();
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Click the "Review Changes" button (git-status-btn in explorer header)
    await page.locator('.git-status-btn').click();

    // Wait for the Changes panel header to appear
    await expect(page.locator('.panel-title')).toHaveText('Changes', { timeout: 10000 });

    // The Changes panel lists FILES; the diff (and its clickable rows) only renders for
    // the selected one — asserting on `.diff-row` before this point asserted nothing.
    const fileRow = page.locator('.changes-file-row', { hasText: testFile });
    await expect(fileRow).toBeVisible({ timeout: 10000 });
    await fileRow.click();
    const clickableRows = page.locator('.diff-row-clickable');
    await expect(clickableRows.first()).toBeVisible({ timeout: 10000 });

    // Pick a row that has a visible line number (not the first one which might be a header)
    // Get the line number from the gutter next to the clickable row
    // We'll click on a row and verify the editor opens
    const rowCount = await clickableRows.count();
    const targetRow = clickableRows.nth(Math.min(5, rowCount - 1)); // ~line 6 area
    await targetRow.click();

    // The Review Changes panel should close and the editor should open
    // Wait for the editor to appear (branch-editor)
    await expect(page.locator('.branch-editor')).toBeVisible({ timeout: 10000 });

    // Verify the file is loaded (loading state disappears)
    await expect(page.locator('.editor-placeholder:has-text("Loading file")')).not.toBeVisible({ timeout: 10000 });

    // Verify the file name is shown in the tab bar
    await expect(page.locator('.editor-tab-name')).toContainText(testFile);

    // Verify CodeMirror editor is present
    await expect(page.locator('.cm-editor')).toBeVisible({ timeout: 5000 });

    // Verify the cursor is on a specific line (not line 1)
    // The active line gutter should show a line number > 1
    // More than one editor can be mounted (tabs keep their instances), so read the first.
    const activeLineGutter = page.locator('.cm-activeLineGutter').first();
    await expect(activeLineGutter).toBeVisible({ timeout: 5000 });
    const activeLineNumber = await activeLineGutter.textContent();
    expect(activeLineNumber).toBeTruthy();
    const lineNum = parseInt(activeLineNumber!.trim(), 10);
    // Should be on a line > 1 (we clicked on a row ~5 lines in)
    expect(lineNum).toBeGreaterThan(1);

    // Clean up
    execSync(`rm -f ${filePath}`);
    execSync(`git -C ${projectDir} reset -q -- ${testFile}`, { stdio: 'ignore' });
  });

});
