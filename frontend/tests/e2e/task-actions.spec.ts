import { test, expect } from '@playwright/test';

test.describe('Task Actions', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should not show Start/Stop buttons in header', async ({ page }) => {
    // Create a task first
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', 'Task for conversational start');
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Should NOT show legacy Start/Stop buttons (conversational model)
    const startButtons = page.locator('[title="Start task"]');
    const stopButtons = page.locator('[title="Stop task"]');
    await expect(startButtons).not.toBeVisible();
    await expect(stopButtons).not.toBeVisible();

    // Should show chat interface (form for existing tasks, start button for new tasks)
    const hasChatForm = await page.locator('.chat-form').isVisible().catch(() => false);
    const hasStartButton = await page.locator('.btn-start-task').isVisible().catch(() => false);
    expect(hasChatForm || hasStartButton).toBe(true);
  });

  test('should show Delete button', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Delete button should be visible
    const deleteButton = page.locator('.btn-icon[title="Delete task"]');
    await expect(deleteButton).toBeVisible();
  });

  test('should confirm before deleting task', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Accept the confirmation dialog
    page.on('dialog', dialog => dialog.accept());

    // Click delete button
    await page.click('.btn-icon[title="Delete task"]');

    // Should navigate back to board
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
  });

  test('should cancel delete task', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    const initialTaskCount = await page.locator('.card').count();

    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Reject the confirmation dialog
    page.on('dialog', dialog => dialog.dismiss());

    // Click delete button
    await page.click('.btn-icon[title="Delete task"]');

    // Should still be on task detail page
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });

  test('should show Backlog column title in context', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Context column should show Backlog title
    await expect(page.locator('.column-context .column-title')).toContainText('Backlog');
  });

  test('should show task count in context column', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Column count should be visible
    await expect(page.locator('.column-context .column-count')).toBeVisible();
  });

  test('should show Start Task button for new tasks', async ({ page }) => {
    // Count cards before creation to identify the new one
    const countBefore = await page.locator('.card').count();

    // Create a new task
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Start task test ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for card count to increase (new card appeared)
    await expect(async () => {
      const count = await page.locator('.card').count();
      expect(count).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    // Open the newly created task (the one after the previous count)
    await page.locator('.card').nth(countBefore).click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // New task should show the Start Task button
    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });

    // Start button text should contain "Start"
    await expect(startButton).toContainText('Start');
  });
});
