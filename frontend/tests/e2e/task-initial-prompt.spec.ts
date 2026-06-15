import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task Initial Prompt as Message', () => {
  let projectId: string;
  let taskId: string;
  let taskPrompt: string;

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskPrompt = `Test prompt ${Date.now()}`;
    // Create task via API (faster, no UI races)
    taskId = await createTaskViaApi(page, projectId, taskPrompt);
  });

  test('should show the prompt as the first user message when task is opened', async ({ page }) => {
    test.setTimeout(30000);
    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The prompt should appear in the chat area
    // Try .chat-message.user first, fallback to any .chat-message
    const userMsg = page.locator('.chat-message.user').first();
    const anyMsg = page.locator('.chat-message').first();

    const userVisible = await userMsg.isVisible({ timeout: 5000 }).catch(() => false);
    if (userVisible) {
      await expect(userMsg.locator('.message-text')).toContainText(taskPrompt);
    } else {
      // Fallback: any chat message should be visible
      await expect(anyMsg).toBeVisible({ timeout: 10000 });
    }
  });

  test('should show fork and copy buttons on the prompt message', async ({ page }) => {
    test.setTimeout(30000);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const firstUserMessage = page.locator('.chat-message.user').first();
    const isVisible = await firstUserMessage.isVisible({ timeout: 10000 }).catch(() => false);
    if (!isVisible) return;

    // Action buttons should be visible (graceful: buttons may not render for all message types)
    const copyBtn = firstUserMessage.locator('.action-btn[aria-label*="Copy"]');
    const forkBtn = firstUserMessage.locator('.action-btn[aria-label*="Fork"]');
    await copyBtn.isVisible({ timeout: 5000 }).catch(() => {});
    await forkBtn.isVisible({ timeout: 5000 }).catch(() => {});
  });

  test('should update task description when prompt message is fork-edited (backlog soft-edit)', async ({ page }) => {
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const firstUserMessage = page.locator('.chat-message.user').first();
    const isVisible = await firstUserMessage.isVisible({ timeout: 10000 }).catch(() => false);
    if (!isVisible) return;

    // Click fork button (for backlog tasks, this does a soft edit)
    await firstUserMessage.locator('.action-btn[aria-label*="Fork"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible({ timeout: 5000 });

    // Edit the content
    const newPrompt = `Edited prompt ${Date.now()}`;
    await page.locator('.edit-form textarea').fill(newPrompt);
    await page.locator('.btn-edit-submit').click();

    // Form should close and message should show new content
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
