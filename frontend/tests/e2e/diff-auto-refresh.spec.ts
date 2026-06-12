import { test, expect } from '@playwright/test';
import { getProjectId, getLastTaskId } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Diff Auto-Refresh', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should load diff when task detail opens', async ({ page }) => {
    // Open task detail
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Wait for WebSocket to connect
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

    // Verify the diff panel is present
    await expect(page.locator('.diff-content')).toBeVisible();

    // Should show either empty state, loading, or diff content
    const diffElements = await page.locator('.diff-empty, .diff-loading, .diff-scroll').count();
    expect(diffElements).toBeGreaterThan(0);
  });

  test('should load diff on mount and show content', async ({ page }) => {
    // Intercept the diff API to return a mock JSON diff (new API format)
    await page.route('**/api/projects/*/tasks/*/diff', async (route) => {
      const mockDiff = `diff --git a/test.txt b/test.txt
--- a/test.txt
+++ b/test.txt
@@ -1,2 +1,3 @@
 line one
+added line
 line two
`;
      await route.fulfill({
        body: JSON.stringify({ diff: mockDiff, untracked: [] }),
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    });

    // Move a task to in_progress so loadDiff() is called on mount
    // (backlog tasks skip diff loading because they have no worktree)
    const projectId = await getProjectId(page);
    const taskId = await getLastTaskId(page, projectId);
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
    }, { pid: projectId, tid: taskId });

    // Open the specific task via URL (the one moved to in_progress)
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Wait for WebSocket to connect
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 10000 });

    // Diff should load on mount and render with structured content
    await expect(page.locator('.diff-scroll')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.diff-file-header')).toBeVisible();
    await expect(page.locator('.diff-row-added')).toBeVisible();
  });
});
