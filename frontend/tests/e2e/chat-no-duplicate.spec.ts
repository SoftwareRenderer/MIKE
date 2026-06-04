import { test, expect } from '@playwright/test';

test.describe('Chat Message Deduplication', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should not duplicate user message after sending', async ({ page }) => {
    // Open a task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Wait for chat interface to be ready
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible({ timeout: 5000 });

    // If this is a new task with a start button, click it first to get into chat mode
    const hasStartButton = await page.locator('.btn-start-task').isVisible().catch(() => false);
    if (hasStartButton) {
      await page.click('.btn-start-task');
      // Wait for task to start and chat form to appear
      await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    }

    // Count initial messages
    const initialMsgCount = await page.locator('.chat-message.user').count();

    // Send a message
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const testMsg = `No dup test ${Date.now()}`;
    await chatTextarea.fill(testMsg);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait a moment for the server echo to arrive
    await page.waitForTimeout(2000);

    // Count user messages - should be exactly initial + 1 (no duplicate)
    const userMessages = page.locator('.chat-message.user');
    const userMsgCount = await userMessages.count();
    expect(userMsgCount).toBe(initialMsgCount + 1);

    // Verify the message content appears only once
    const matchingMessages = page.locator('.chat-message.user:has-text("' + testMsg + '")');
    const matchingCount = await matchingMessages.count();
    expect(matchingCount).toBe(1);
  });
});
