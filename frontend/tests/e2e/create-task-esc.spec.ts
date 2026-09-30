import { test, expect } from '@playwright/test';
import {
  getProjectId, createTaskViaApi, navigateToTask, isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// CREATE TASK MODAL — ESCAPE BEHAVIOR
// ============================================================
// When the Create Task modal is opened from Task View, pressing Escape must
// ONLY close the dialog (not the Task View behind it), and must warn that
// changes will be lost when the description isn't blank.

test.describe.serial('Create Task modal Escape behavior', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('Esc with a non-blank description warns before closing and keeps the Task View open', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `esc-warn-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Open Create Task from within the Task View (Board Overview)
    await page.locator('.task-item-add').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    // Type a description, then press Escape
    await page.fill('#task-description', 'unsaved description');
    await page.keyboard.press('Escape');

    // A confirmation dialog warns that changes will be lost
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.confirm-message')).toContainText('changes will be lost');

    // The modal and the Task View both remain open
    await expect(page.locator('#task-description')).toBeVisible();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // "Keep editing" keeps the modal open
    await page.locator('.btn-cancel').click();
    await expect(page.locator('.confirm-message')).not.toBeVisible();
    await expect(page.locator('#task-description')).toBeVisible();

    // Escape again, then confirm discard — the modal closes, Task View stays
    await page.keyboard.press('Escape');
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });

  test('Esc with a blank description closes the modal immediately without warning', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `esc-blank-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await page.locator('.task-item-add').first().click();
    await expect(page.locator('#task-description')).toBeVisible({ timeout: 5000 });

    // No description — Escape closes the modal immediately, no warning
    await page.keyboard.press('Escape');
    await expect(page.locator('#task-description')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.confirm-message')).not.toBeVisible();

    // The Task View must remain open (Escape must not close it)
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });
});
