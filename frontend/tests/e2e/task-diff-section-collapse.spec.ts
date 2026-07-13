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
        const entries = readdirSync(worktreesDir);
        for (const entry of entries) {
            const fullPath = join(worktreesDir, entry);
            if (statSync(fullPath).isDirectory()) {
                return fullPath;
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

test.describe.serial('Diff Section Collapse', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('collapsing a section collapses all child files, file headers remain visible', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const files = [
      `section-collapse-test-${uniqueId}-a.txt`,
      `section-collapse-test-${uniqueId}-b.txt`,
      `section-collapse-test-${uniqueId}-c.txt`,
    ];
    for (const name of files) {
      writeFileSync(join(worktreeDir, name), `Content of ${name}\nLine 2\nLine 3\n`);
    }

    try {
      await page.locator('.card').first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      // Wait for diff rows to appear
      await expect(async () => {
        const diffRows = await page.locator('.diff-row').count();
        expect(diffRows).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      await expect(async () => {
        const fileHeaders = await page.locator('.diff-file-header').count();
        expect(fileHeaders).toBeGreaterThanOrEqual(3);
      }).toPass({ timeout: 10000 });

      const unstagedHeader = page.locator('.diff-section-header.diff-section-unstaged');
      await expect(unstagedHeader).toBeVisible();

      // Verify files are initially expanded
      await expect(async () => {
        const expandedHeaders = await page.locator('.diff-file-header[aria-expanded="true"]').count();
        expect(expandedHeaders).toBeGreaterThanOrEqual(3);
      }).toPass({ timeout: 5000 });

      // Verify diff rows are visible (file content shown)
      const diffRowsBefore = await page.locator('.diff-row').count();
      expect(diffRowsBefore).toBeGreaterThan(0);

      // Click the unstaged section header to collapse all child files
      await unstagedHeader.click();

      // Section body should still be visible (file headers remain)
      await expect(page.locator('.diff-section-body')).toBeVisible();

      // File headers should still be visible
      await expect(async () => {
        const fileHeaders = await page.locator('.diff-file-header').count();
        expect(fileHeaders).toBeGreaterThanOrEqual(3);
      }).toPass({ timeout: 5000 });

      // All file headers should now be collapsed (aria-expanded="false")
      await expect(async () => {
        const collapsedHeaders = await page.locator('.diff-file-header[aria-expanded="false"]').count();
        expect(collapsedHeaders).toBeGreaterThanOrEqual(3);
      }).toPass({ timeout: 5000 });

      // Diff rows should be hidden (file content collapsed)
      await expect(async () => {
        const diffRowsAfter = await page.locator('.diff-row').count();
        expect(diffRowsAfter).toBeLessThan(diffRowsBefore);
      }).toPass({ timeout: 5000 });
    } finally {
      for (const name of files) {
        try { execSync(`rm -f "${join(worktreeDir, name)}"`); } catch { /* ignore */ }
      }
    }
  });

  test('expanding a section expands all child files individually', async ({ page }) => {
    const taskInfo = getTaskInfo();
    const worktreeDir = findWorktreeDir();
    if (!taskInfo || !worktreeDir) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const uniqueId = Date.now();
    const files = [
      `section-expand-test-${uniqueId}-a.txt`,
      `section-expand-test-${uniqueId}-b.txt`,
    ];
    for (const name of files) {
      writeFileSync(join(worktreeDir, name), `Content of ${name}\nLine 2\n`);
    }

    try {
      await page.locator('.card').first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      await expect(async () => {
        const diffRows = await page.locator('.diff-row').count();
        expect(diffRows).toBeGreaterThan(0);
      }).toPass({ timeout: 10000 });

      await expect(async () => {
        const fileHeaders = await page.locator('.diff-file-header').count();
        expect(fileHeaders).toBeGreaterThanOrEqual(2);
      }).toPass({ timeout: 10000 });

      const unstagedHeader = page.locator('.diff-section-header.diff-section-unstaged');
      await expect(unstagedHeader).toBeVisible();

      // Collapse the section
      await unstagedHeader.click();
      await expect(async () => {
        const collapsedHeaders = await page.locator('.diff-file-header[aria-expanded="false"]').count();
        expect(collapsedHeaders).toBeGreaterThanOrEqual(2);
      }).toPass({ timeout: 5000 });

      // Expand the section again — all files should be expanded
      await unstagedHeader.click();

      // All file headers should have aria-expanded="true"
      await expect(async () => {
        const expandedHeaders = await page.locator('.diff-file-header[aria-expanded="true"]').count();
        expect(expandedHeaders).toBeGreaterThanOrEqual(2);
      }).toPass({ timeout: 5000 });

      // Diff rows should be visible again
      await expect(async () => {
        const diffRows = await page.locator('.diff-row').count();
        expect(diffRows).toBeGreaterThan(0);
      }).toPass({ timeout: 5000 });
    } finally {
      for (const name of files) {
        try { execSync(`rm -f "${join(worktreeDir, name)}"`); } catch { /* ignore */ }
      }
    }
  });
});
