import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch } from './helpers';

test.describe.serial('Message copy and edit actions', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `message-actions-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForSendButton(page);
  });

  test('should show visual feedback when copying a message', async ({ page }) => {
    const countBefore = await page.locator('.chat-message.user').count();

    const msgContent = `copy test message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    // Find the copy button on the newly added message
    const copyBtn = page.locator('.chat-message.user').nth(countBefore).locator('[aria-label="Copy message"]');
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();

    await expect(copyBtn).toHaveClass(/copied/);
    await page.waitForTimeout(2500);
    await expect(copyBtn).not.toHaveClass(/copied/);
  });

  test('should show edit button on user messages', async ({ page }) => {
    const countBefore = await page.locator('.chat-message.user').count();

    const msgContent = `edit test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msgContent },
    });

    await expect(async () => {
      expect(await page.locator('.chat-message.user').count()).toBeGreaterThan(countBefore);
    }).toPass({ timeout: 10000 });

    await waitForSendButton(page);

    // Verify the edit/fork button is visible on the user message
    const editBtn = page.locator('.chat-message.user').nth(countBefore).locator('[aria-label="Start new thread from this message"]');
    await expect(editBtn).toBeVisible();
  });

  test('first user message should have no delete button', async ({ page }) => {
    const firstMessage = page.locator('.chat-message.user').first();
    await expect(firstMessage).toBeVisible({ timeout: 10000 });

    await waitForSendButton(page);

    const deleteBtn = firstMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(deleteBtn).not.toBeVisible();
  });
});
