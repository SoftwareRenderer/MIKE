import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });

// --- Tests: Board overview sections ---

test.describe('Board overview sections in task detail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show all 4 sections in the side panel', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `overview-sections-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // All 4 section headers should be visible
    const sections = page.locator('.section-header');
    await expect(sections).toHaveCount(4, { timeout: 5000 });

    // Verify section titles in order
    const sectionTitles = page.locator('.section-title');
    await expect(sectionTitles.nth(0)).toContainText('Backlog');
    await expect(sectionTitles.nth(1)).toContainText('In Progress');
    await expect(sectionTitles.nth(2)).toContainText('Review');
    await expect(sectionTitles.nth(3)).toContainText('Done');
  });

  test('should show sections even when empty', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Create a task in backlog (default status)
    const taskId = await createTaskViaApi(page, projectId, `empty-sections-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // All 4 section groups should be visible (even In Progress, Review, Done are empty)
    const sectionGroups = page.locator('.section-group');
    await expect(sectionGroups).toHaveCount(4, { timeout: 5000 });

    // All 4 section headers should be visible
    const sectionHeaders = page.locator('.section-header');
    await expect(sectionHeaders).toHaveCount(4, { timeout: 5000 });
  });

  test('should have collapsible sections with chevron', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `collapse-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Chevron icons should be present
    const chevrons = page.locator('.section-chevron');
    await expect(chevrons).toHaveCount(4);

    // Section headers should be buttons with aria-expanded
    const sectionHeaders = page.locator('.section-header');
    // Use the In Progress section (index 1) for collapse test
    // because the current task's section (Backlog) cannot be collapsed
    await expect(sectionHeaders.nth(1)).toHaveAttribute('aria-expanded', 'true');

    // Click to collapse the In Progress section
    await sectionHeaders.nth(1).click();
    await expect(sectionHeaders.nth(1)).toHaveAttribute('aria-expanded', 'false');

    // Click again to expand
    await sectionHeaders.nth(1).click();
    await expect(sectionHeaders.nth(1)).toHaveAttribute('aria-expanded', 'true');
  });
});

// --- Tests: Board overview scroll behavior ---

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
