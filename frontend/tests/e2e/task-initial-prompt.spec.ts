import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task Initial Prompt as Message', () => {
  let projectId: string;
  let taskId: string;
  let taskPrompt: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskPrompt = `Test prompt ${Date.now()}`;
    // Create task via API (faster, no UI races)
    taskId = await createTaskViaApi(page, projectId, taskPrompt);
  });

  test('should show the prompt as the first user message when task is opened', async ({ page }) => {
    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The prompt should appear as a user message in the chat
    const firstUserMessage = page.locator('.chat-message.user').first();
    await expect(firstUserMessage).toBeVisible({ timeout: 10000 });
    await expect(firstUserMessage.locator('.message-text')).toContainText(taskPrompt);
  });

  test('should show edit and copy buttons on the prompt message', async ({ page }) => {
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const firstUserMessage = page.locator('.chat-message.user').first();
    await expect(firstUserMessage).toBeVisible({ timeout: 10000 });

    // Action buttons should be visible
    await expect(firstUserMessage.locator('.action-btn[aria-label*="Copy"]')).toBeVisible();
    await expect(firstUserMessage.locator('.action-btn[aria-label*="Edit"]')).toBeVisible();
  });

  test('should update task description when prompt message is edited', async ({ page }) => {
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const firstUserMessage = page.locator('.chat-message.user').first();
    await expect(firstUserMessage).toBeVisible({ timeout: 10000 });

    // Click edit button
    await firstUserMessage.locator('.action-btn[aria-label*="Edit"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible({ timeout: 5000 });

    // Edit the content
    const newPrompt = `Edited prompt ${Date.now()}`;
    await page.locator('.edit-form textarea').fill(newPrompt);
    await page.locator('.btn-edit-submit').click();

    // Edit form should close and message should show new content
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });
    await expect(firstUserMessage.locator('.message-text')).toContainText(newPrompt);

    // Verify the task prompt was updated via API
    const updatedPrompt = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.prompt;
    }, { pid: projectId, tid: taskId });
    expect(updatedPrompt).toBe(newPrompt);
  });
});
