import { test, expect } from '@playwright/test';
import { getProjectId } from './helpers';

test.describe('Task View Explorer Link', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open explorer with worktree path when clicking project path in task header', async ({ page }) => {
    const projectId = await getProjectId(page);
    if (!projectId) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    // Open project and click on first card to open task detail
    await page.locator('.project-item').first().click();
    await page.waitForSelector('.card', { timeout: 10000 });

    // If no cards exist, skip (nothing to test)
    const cardCount = await page.locator('.card').count();
    if (cardCount === 0) {
      return;
    }

    // Get the task ID from the first card
    const taskId = await page.locator('.card').first().getAttribute('data-task-id');
    if (!taskId) {
      return;
    }

    // Click first card to open task detail
    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Verify the explorer button is visible and clickable
    const explorerBtn = page.locator('.explorer-btn');
    await expect(explorerBtn).toBeVisible();

    // Click the explorer button
    await explorerBtn.click();

    // Verify explorer view opens
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Verify the explorer shows the task's worktree path (contains task ID)
    const explorerPath = page.locator('.explorer-view .project-path');
    await expect(explorerPath).toBeVisible();
    const pathText = await explorerPath.textContent();
    expect(pathText).toContain(taskId);
  });
});
