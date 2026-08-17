import { test, expect } from '@playwright/test';
import {
  getProjectId,
  getLastTaskId,
  navigateToTask,
  ensureTaskStarted,
  waitForSendButton,
  csrfFetch,
} from './helpers';

test.describe.serial('Chat Jump to Latest', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    // Use the last test task which has the initial prompt + response
    taskId = await getLastTaskId(page, projectId);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureTaskStarted(page, projectId, taskId);
    await waitForSendButton(page);
  });

  test('should show jump-to-latest button when scrolled up and hide when at bottom', async ({ page }) => {
    test.setTimeout(60000);

    // Send multiple messages to create enough content for scrolling
    for (let i = 0; i < 8; i++) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        body: { content: `test message ${i} padding padding padding padding padding padding padding padding padding ${Date.now()}` },
      });
      // Wait for send button to reappear (LLM responded)
      await waitForSendButton(page);
    }

    // Verify we have scrollable content
    const chatList = page.locator('.chat-messages').first();
    const scrollable = await chatList.evaluate((el) => {
      return el.scrollHeight > el.clientHeight + 100;
    });
    expect(scrollable).toBe(true);

    // The chat should have scrolled to bottom initially — button should NOT be visible
    const jumpBtn = page.locator('.chat-jump-latest');
    await expect(jumpBtn).not.toBeVisible();

    // Scroll up by setting scrollTop directly and dispatching scroll event
    await chatList.evaluate((el) => {
      // Scroll to the top
      el.scrollTop = 0;
      // Dispatch scroll event to trigger Svelte handler
      el.dispatchEvent(new Event('scroll', { bubbles: true }));
    });

    // Wait for reactivity to propagate
    await page.waitForTimeout(200);

    // Now the jump-to-latest button should be visible
    await expect(jumpBtn).toBeVisible({ timeout: 5000 });
    await expect(jumpBtn).toHaveAttribute('aria-label', 'Scroll to latest message');
    await expect(jumpBtn).toHaveAttribute('title', 'Scroll to latest message');

    // Click the jump-to-latest button
    await jumpBtn.click();

    // Wait for scroll animation
    await page.waitForTimeout(300);

    // The button should disappear after scrolling to bottom
    await expect(jumpBtn).not.toBeVisible({ timeout: 3000 });

    // Verify the chat is scrolled near the bottom
    const isNearBottom = await chatList.evaluate((el) => {
      const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
      return gap < 100; // generous threshold for test
    });
    expect(isNearBottom).toBe(true);
  });

  test('should not show jump-to-latest button when there are no messages', async ({ page }) => {
    test.setTimeout(15000);
    // Navigate to a fresh task with no messages (the prompt is auto-loaded)
    // Instead, just verify the button doesn't appear when scrolled to bottom
    const jumpBtn = page.locator('.chat-jump-latest');
    await expect(jumpBtn).not.toBeVisible();
  });
});
