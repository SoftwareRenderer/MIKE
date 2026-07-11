import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton, csrfFetch } from './helpers';

test.describe.serial('First user message has no delete button', () => {
  test('first user message hides delete button, second shows it', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `test-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForSendButton(page);

    // Send the first user message via API (bypasses LLM wait)
    const msg1 = `first message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg1 },
    });

    // Wait for the first user message to appear
    await expect(page.locator('.chat-message.user')).toHaveCount(1, { timeout: 10000 });

    // Wait for LLM response to complete so send button reappears
    await waitForSendButton(page);

    // The first user message should NOT have a delete button
    const firstMessage = page.locator('.chat-message.user').first();
    const firstDeleteBtn = firstMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(firstDeleteBtn).not.toBeVisible();

    // Send a second user message
    const msg2 = `second message ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: msg2 },
    });

    // Wait for the second user message to appear
    await expect(page.locator('.chat-message.user')).toHaveCount(2, { timeout: 10000 });

    // Wait for LLM response to complete
    await waitForSendButton(page);

    // The second user message SHOULD have a delete button
    const secondMessage = page.locator('.chat-message.user').last();
    const secondDeleteBtn = secondMessage.locator('[aria-label="Delete message and all following messages"]');
    await expect(secondDeleteBtn).toBeVisible({ timeout: 5000 });

    // The first user message should STILL NOT have a delete button
    await expect(firstDeleteBtn).not.toBeVisible();
  });
});
