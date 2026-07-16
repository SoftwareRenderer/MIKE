import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, getTaskData } from './helpers';

test.describe.serial('Edit Task Modal', () => {
  let projectId: string;
  let taskId: string;
  const originalTitle = `edit-test-task-${Date.now()}`;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should open edit modal with pre-filled task name', async ({ page }) => {
    // Create a fresh task via API
    taskId = await createTaskViaApi(page, projectId, originalTitle);
    expect(taskId).not.toBe('');

    // Navigate to the task detail page
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Find the card for this task in the side panel (it will be selected)
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    // Click the edit button (pencil icon with aria-label="Edit task")
    const editButton = card.locator('button[aria-label="Edit task"]');
    await expect(editButton).toBeVisible();
    await editButton.click();

    // Verify the EditTaskModal appears
    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Verify modal title
    const modalTitle = page.locator('#modal-title');
    await expect(modalTitle).toContainText('Edit Task');

    // Verify the title input is pre-filled with the original task name
    const titleInput = page.locator('#edit-task-title-input');
    await expect(titleInput).toBeVisible();
    await expect(titleInput).toHaveValue(originalTitle);

    // Close the modal without saving (click Cancel to clean up for next test)
    const cancelButton = page.locator('.modal-footer .btn-secondary');
    await cancelButton.click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });
  });

  test('should rename a task and reflect on board and API', async ({ page }) => {
    // Navigate to the task (created in previous test)
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Find the card and open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Change the title
    const newTitle = `renamed-task-${Date.now()}`;
    const titleInput = page.locator('#edit-task-title-input');
    await titleInput.clear();
    await titleInput.fill(newTitle);

    // Click Save
    const saveButton = page.locator('.modal-footer .btn-primary');
    await expect(saveButton).toBeVisible();
    await saveButton.click();

    // Modal should close
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Wait for the card to update with the new title
    const cardTitle = page.locator('.card.selected .card-title-text');
    await expect(cardTitle).toContainText(newTitle, { timeout: 5000 });

    // Verify the new title via API
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.title).toBe(newTitle);

    // Also verify via board API
    const boardTitle = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      const task = board.tasks.find((t: any) => t.id === tid);
      return task?.title || '';
    }, { pid: projectId, tid: taskId });
    expect(boardTitle).toBe(newTitle);
  });

  test('should cancel without saving and preserve original name', async ({ page }) => {
    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Get the current title from API (should be the renamed title from previous test)
    const currentData = await getTaskData(page, projectId, taskId);
    const currentTitle = currentData.title as string;

    // Open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Change the title to something different
    const titleInput = page.locator('#edit-task-title-input');
    await titleInput.clear();
    await titleInput.fill(`cancelled-change-${Date.now()}`);

    // Click Cancel
    const cancelButton = page.locator('.modal-footer .btn-secondary');
    await expect(cancelButton).toBeVisible();
    await cancelButton.click();

    // Modal should close
    await expect(modal).not.toBeVisible({ timeout: 5000 });

    // Verify the card title is unchanged (still the renamed title, not the cancelled change)
    const cardTitle = page.locator('.card.selected .card-title-text');
    await expect(cardTitle).toContainText(currentTitle, { timeout: 5000 });

    // Verify via API that the title was not changed
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.title).toBe(currentTitle);
  });

  test('should reject empty name and keep modal open', async ({ page }) => {
    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Open the edit modal
    const card = page.locator('.card.selected');
    await expect(card).toBeVisible({ timeout: 5000 });

    const editButton = card.locator('button[aria-label="Edit task"]');
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Get current title for later comparison
    const currentData = await getTaskData(page, projectId, taskId);
    const currentTitle = currentData.title as string;

    // Clear the title input completely
    const titleInput = page.locator('#edit-task-title-input');
    await titleInput.clear();

    // Verify the save button is disabled when title is empty
    const saveButton = page.locator('.modal-footer .btn-primary');
    await expect(saveButton).toBeDisabled();

    // Try to save with empty title by pressing Enter in the input
    await titleInput.press('Enter');

    // Modal should still be open (save was rejected)
    await expect(modal).toBeVisible();

    // Verify the task title was not changed via API
    const taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.title).toBe(currentTitle);

    // Also verify the title input still exists and is empty (modal didn't reset it)
    await expect(titleInput).toBeVisible();

    // Clean up: cancel the modal
    const cancelButton = page.locator('.modal-footer .btn-secondary');
    await cancelButton.click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });
  });
});
