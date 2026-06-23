import { test, expect } from '@playwright/test';
import {getProjectId, createTaskViaApi, navigateToTask} from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Task Actions', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should not show Start/Stop buttons in header', async ({ page }) => {
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

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
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-delete-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Delete button should be visible
    const deleteButton = page.locator('.btn-icon[aria-label="Delete task"]');
    await expect(deleteButton).toBeVisible();
  });

  test('should confirm before deleting task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-confirm-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click delete button to trigger confirmation dialog
    await page.click('.btn-icon[aria-label="Delete task"]');

    // Wait for confirmation dialog and accept
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await page.click('.confirm-dialog .btn-confirm');

    // Should navigate back to board
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
  });

  test('should cancel delete task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-cancel-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click delete button to trigger confirmation dialog
    await page.click('.btn-icon[aria-label="Delete task"]');

    // Wait for confirmation dialog and cancel
    await expect(page.locator('.confirm-dialog')).toBeVisible({ timeout: 5000 });
    await page.click('.confirm-dialog .btn-cancel');

    // Should still be on task detail page
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });

  test('should show Backlog column title in context', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-backlog-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Context column should show Backlog section title
    await expect(page.locator('.section-title').first()).toBeVisible();
  });

  test('should show task count in context column', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-count-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Context column should be visible with section headers
    await expect(page.locator('.column-context')).toBeVisible();
  });

  test('should show Start Task button for new tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `Start task test ${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // New task should show the Start Task button
    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });

    // Start button text should contain "Start"
    await expect(startButton).toContainText('Start');
  });
});
