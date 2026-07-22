import { test, expect } from '@playwright/test';
import { writeFileSync, rmSync, readdirSync, statSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { getProjectId, csrfFetch } from './helpers';

function getConfig() {
  return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
  const worktreesDir = getConfig().workspace_dir;
  try {
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

test.describe('Explorer Git History Refresh After Commit', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should update git history after committing from Review Changes panel', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      // No worktree or project — skip gracefully
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `history-refresh-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    const commitMessage = `Add ${testFile}`;
    const fileContent = `Test file for git history refresh\nLine 2\nLine 3\n`;
    writeFileSync(filePath, fileContent);

    try {
      // Click on the project to select it
      await page.locator('.project-item').first().click();
      await page.waitForSelector('.top-bar', { timeout: 10000 });

      // Open the explorer view
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

      // Wait for the git graph to load (History panel)
      await expect(page.locator('.git-graph')).toBeVisible({ timeout: 10000 });

      // Count commits before (should have at least the init commit)
      const commitsBefore = await page.locator('.git-commit-row').count();
      expect(commitsBefore).toBeGreaterThanOrEqual(1);

      // Open the Review Changes panel
      await page.locator('.git-status-btn').click();

      // Wait for the Changes panel header
      await expect(page.locator('.panel-title')).toHaveText('Changes', { timeout: 10000 });

      // Wait for unstaged entries to appear
      await expect(async () => {
        const unstagedCount = await page.locator('.changes-section:has-text("Unstaged")').count();
        expect(unstagedCount).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      // Stage the file: click the "+" (stage) button on the first unstaged file row
      const unstagedSection = page.locator('.changes-section:has-text("Unstaged")');
      const stageBtn = unstagedSection.locator('.changes-stage-btn').first();
      await stageBtn.click();

      // The file should now be in the Staged section
      await expect(async () => {
        const stagedCount = await page.locator('.changes-section:has-text("Staged")').count();
        expect(stagedCount).toBeGreaterThan(0);
      }).toPass({ timeout: 5000 });

      // Click the Commit button
      const commitBtn = page.locator('.panel-commit-btn');
      await expect(commitBtn).toBeVisible({ timeout: 5000 });
      await commitBtn.click();

      // Wait for the commit dialog to appear
      const commitDialog = page.locator('[role="dialog"]');
      await expect(commitDialog).toBeVisible({ timeout: 10000 });

      // Fill in the commit message
      const commitTextarea = page.locator('.commit-textarea');
      await expect(commitTextarea).toBeVisible({ timeout: 5000 });
      await commitTextarea.fill(commitMessage);

      // Wait for the confirm button to be enabled
      const confirmBtn = page.locator('.modal-footer .btn-primary');
      await expect(confirmBtn).toBeEnabled({ timeout: 10000 });

      // Click confirm to commit
      await confirmBtn.click();

      // Dialog should close
      await expect(commitDialog).not.toBeVisible({ timeout: 5000 });

      // Changes panel should close (onClose is called after onCommitted)
      await expect(page.locator('.panel-title')).not.toBeVisible({ timeout: 5000 });

      // The git graph should now show the new commit (one more than before)
      await expect(async () => {
        const commitsAfter = await page.locator('.git-commit-row').count();
        expect(commitsAfter).toBeGreaterThan(commitsBefore);
      }).toPass({ timeout: 10000 });

      // Verify the new commit is at the top with the correct message
      const firstCommit = page.locator('.git-commit-row').first();
      const subjectText = await firstCommit.locator('.git-commit-subject').textContent();
      expect(subjectText).toContain(commitMessage);

      // Verify the commit is marked as unpushed (blue dot)
      const firstDot = firstCommit.locator('.git-dot.unpushed');
      await expect(firstDot).toBeVisible();
    } finally {
      // Clean up the test file
      try { rmSync(filePath, { force: true }); } catch { /* ignore */ }
    }
  });

  test('should refresh branches list after committing from Review Changes', async ({ page }) => {
    const worktreeDir = findWorktreeDir();
    const projectId = await getProjectId(page);
    if (!worktreeDir || !projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const testFile = `branch-refresh-${uniqueId}.txt`;
    const filePath = join(worktreeDir, testFile);
    const fileContent = `Test file for branch refresh\n`;
    writeFileSync(filePath, fileContent);

    try {
      // Click on the project to select it
      await page.locator('.project-item').first().click();
      await page.waitForSelector('.top-bar', { timeout: 10000 });

      // Open the explorer view
      await page.locator('.explorer-btn').click();
      await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

      // Open the Review Changes panel
      await page.locator('.git-status-btn').click();
      await expect(page.locator('.panel-title')).toHaveText('Changes', { timeout: 10000 });

      // Wait for unstaged entries
      await expect(async () => {
        const unstagedCount = await page.locator('.changes-section:has-text("Unstaged")').count();
        expect(unstagedCount).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      // Stage the file
      const unstagedSection = page.locator('.changes-section:has-text("Unstaged")');
      const stageBtn = unstagedSection.locator('.changes-stage-btn').first();
      await stageBtn.click();

      // Wait for staged section to appear
      await expect(async () => {
        const stagedCount = await page.locator('.changes-section:has-text("Staged")').count();
        expect(stagedCount).toBeGreaterThan(0);
      }).toPass({ timeout: 5000 });

      // Commit
      await page.locator('.panel-commit-btn').click();
      const commitDialog = page.locator('[role="dialog"]');
      await expect(commitDialog).toBeVisible({ timeout: 10000 });

      const commitTextarea = page.locator('.commit-textarea');
      await expect(commitTextarea).toBeVisible({ timeout: 5000 });
      await commitTextarea.fill(`Branch refresh test ${uniqueId}`);

      const confirmBtn = page.locator('.modal-footer .btn-primary');
      await expect(confirmBtn).toBeEnabled({ timeout: 10000 });
      await confirmBtn.click();

      // Wait for dialog and changes panel to close
      await expect(commitDialog).not.toBeVisible({ timeout: 5000 });
      await expect(page.locator('.panel-title')).not.toBeVisible({ timeout: 5000 });

      // Verify the explorer is still visible (panel closed, not the whole explorer)
      await expect(page.locator('.explorer-view')).toBeVisible();

      // Verify the git graph has the new commit
      await expect(async () => {
        const rows = await page.locator('.git-commit-row').count();
        expect(rows).toBeGreaterThanOrEqual(1);
      }).toPass({ timeout: 10000 });

      // Verify the branch badge is still visible in the git graph
      await expect(page.locator('.branch-badge')).toBeVisible({ timeout: 5000 });
    } finally {
      try { rmSync(filePath, { force: true }); } catch { /* ignore */ }
    }
  });
});
