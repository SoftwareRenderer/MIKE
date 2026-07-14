import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Board overview scrolls to current task', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should scroll side board to show current task on desktop', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create multiple tasks in backlog to fill the column
    let targetTaskId = '';
    for (let i = 0; i < 10; i++) {
      targetTaskId = await createTaskViaApi(page, projectId, `scroll-task-${i}-${Date.now()}`);
    }

    // Navigate to the last task created (should be at the bottom of the backlog)
    await navigateToTask(page, targetTaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the board overview to render
    const currentCard = page.locator('.card.selected');
    await expect(currentCard).toBeVisible({ timeout: 5000 });

    // Get the scrollable container (column-context) and the selected card positions
    const isVisible = await page.evaluate(() => {
      const card = document.querySelector('.card.selected');
      if (!card) return false;
      const cardRect = card.getBoundingClientRect();

      // Find the scrollable ancestor within the column-context
      const column = document.querySelector('.column-context');
      if (!column) return false;
      const columnRect = column.getBoundingClientRect();

      // Check if the card's top and bottom are within the column's visible area
      // Allow a small margin (10px) for rendering differences
      const margin = 10;
      const cardTopVisible = cardRect.top >= columnRect.top - margin;
      const cardBottomVisible = cardRect.bottom <= columnRect.bottom + margin;

      // Card is considered visible if it's fully within the viewport
      // OR if it's the closest element to the center of the viewport
      const columnCenter = columnRect.top + columnRect.height / 2;
      const cardCenter = cardRect.top + cardRect.height / 2;
      const distanceToCenter = Math.abs(cardCenter - columnCenter);

      return cardTopVisible && cardBottomVisible || distanceToCenter < columnRect.height / 2;
    });

    expect(isVisible).toBe(true);
  });

  test('should keep current task section always expanded', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create multiple tasks
    const taskId1 = await createTaskViaApi(page, projectId, `scroll-task-a-${Date.now()}`);
    await createTaskViaApi(page, projectId, `scroll-task-b-${Date.now()}`);

    // Navigate to the first task (in backlog)
    await navigateToTask(page, taskId1);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The backlog section should be expanded and show "(current task)" in title
    const backlogHeader = page.locator('.section-header').nth(0);
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'true');
    await expect(backlogHeader).toHaveAttribute('title', 'Backlog (current task)');

    // Clicking the header should NOT collapse it (current task's section is protected)
    await backlogHeader.click();
    await expect(backlogHeader).toHaveAttribute('aria-expanded', 'true');

    // The card should be visible and selected
    const currentCard = page.locator('.card.selected');
    await expect(currentCard).toBeVisible({ timeout: 5000 });
  });
});
