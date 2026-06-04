import { test, expect } from '@playwright/test';

test.describe('Task URL Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should update URL when opening a task', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });

    // Get the task ID from the first card
    const taskId = await page.locator('.card').first().evaluate(el => {
      return el.getAttribute('data-task-id') || '';
    });

    // Click the card to open the task
    await page.locator('.card').first().click();

    // Wait for task detail to be visible
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Check URL contains task parameter
    const url = page.url();
    console.log('URL after opening task:', url);
    expect(url).toContain('?task=');
  });

  test('should clear URL when closing task detail', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('.card').first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Click back button
    await page.click('.btn-back');

    // Wait for board to be visible
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Check URL doesn't contain task parameter
    const url = page.url();
    console.log('URL after closing task:', url);
    expect(url).not.toContain('?task=');
  });

  test('should open task from URL on direct navigation', async ({ page }) => {
    // First, get a task ID
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Get task ID from the board data
    const taskId = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      if (projects.length === 0) return null;
      const boardRes = await fetch(`/api/projects/${projects[0].id}/board`);
      const board = await boardRes.json();
      return board.tasks && board.tasks.length > 0 ? board.tasks[0].id : null;
    });

    console.log('Task ID:', taskId);
    expect(taskId).not.toBeNull();

    if (taskId) {
      // Navigate directly to task URL
      await page.goto(`/?task=${taskId}`);

      // Wait for task detail to be visible directly (no need for timeout wait)
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

      // Check URL still has task parameter
      const url = page.url();
      console.log('URL after direct navigation:', url);
      expect(url).toContain(`?task=${taskId}`);
    }
  });

  test('should handle browser back from task detail', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('.card').first().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Use browser back
    await page.goBack();

    // Board should be visible
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });
});