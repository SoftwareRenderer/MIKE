import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask } from './helpers';

// ============================================================
// CREATE TASK MODAL — INITIAL FOCUS
// ============================================================
// When the Create Task modal is opened, keyboard focus must land in the
// description textarea so the user can start typing immediately — both
// for root tasks (from the board) and subtasks (from the Task View).

test.describe.serial('Create Task modal initial focus', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('focuses the description when opened from the board', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    // Initial focus is the description textarea, not the header close button
    await expect
      .poll(() => page.evaluate(() => document.activeElement?.id))
      .toBe('task-description');
  });

  test('immediate typing lands in the description without any click', async ({ page }) => {
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });
    await expect
      .poll(() => page.evaluate(() => document.activeElement?.id))
      .toBe('task-description');

    // No click — keystrokes must go straight into the description
    await page.keyboard.type('typed without clicking');
    await expect(page.locator('#task-description')).toHaveValue('typed without clicking');

    // Clean up: Escape warns about the unsaved description, confirm discard
    await page.keyboard.press('Escape');
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
  });

  test('Create Subtask modal also focuses the description', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `focus-parent-${Date.now()}`);
    await navigateToTask(page, taskId);

    // Open Create Subtask from within the Task View
    await page.locator('.task-item-add').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    await expect
      .poll(() => page.evaluate(() => document.activeElement?.id))
      .toBe('task-description');
  });
});
