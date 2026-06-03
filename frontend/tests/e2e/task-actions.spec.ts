import { test, expect } from '@playwright/test';

test.describe('Task Actions', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show Start button for backlog task', async ({ page }) => {
    // Create a task first
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', 'Task to start');
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Should show Start button for backlog task
    const startButtons = page.locator('.btn-primary-icon[title="Start task"]');
    await expect(startButtons.first()).toBeVisible();
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
});
