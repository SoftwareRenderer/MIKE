import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

test.describe('Mobile delete task scroll to column', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should center on the deleted task column after delete', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `mobile-scroll-${Date.now()}`);

    // Move task to in_progress via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Scroll board to Backlog (first column) so in_progress is NOT centered
    await page.evaluate(() => {
      const board = document.querySelector('.board');
      if (board) board.scrollLeft = 0;
    });
    await new Promise(r => setTimeout(r, 200));

    // Open task detail
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Delete the task
    await page.click('.icon-btn[aria-label="Delete task"]');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');

    // Wait for board to be visible
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Wait for smooth scroll animation to complete
    await new Promise(r => setTimeout(r, 600));

    // Get scroll state
    const { scrollLeft, boardWidth, colOffset, colWidth } = await page.evaluate(() => {
      const board = document.querySelector('.board');
      const col = document.querySelector('.column[aria-label="In Progress column"]');
      if (!board || !col) return null;
      return {
        scrollLeft: board.scrollLeft,
        boardWidth: board.clientWidth,
        colOffset: col.offsetLeft,
        colWidth: col.clientWidth,
      };
    });

    expect(colOffset).not.toBeNull();

    // Column center should be within the viewport
    const colCenter = (colOffset as number) + (colWidth as number) / 2;
    const viewportStart = (scrollLeft as number) - (colWidth as number) / 2;
    const viewportEnd = (scrollLeft as number) + (boardWidth as number) - (colWidth as number) / 2;
    expect(colCenter as number).toBeGreaterThan(viewportStart);
    expect(colCenter as number).toBeLessThan(viewportEnd);
  });
});
