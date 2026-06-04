import { test, expect } from '@playwright/test';
import { ensureChatForm } from './helpers';

test.describe('Chat Message Deduplication', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should not duplicate user message after sending', async ({ page }) => {
    // Create a fresh task to avoid state pollution from other tests
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `No dup test task ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the newly created task (last card)
    await page.locator('.card').last().click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Ensure chat form is ready (handles new-task "Start Task" state)
    await ensureChatForm(page);

    // Count initial messages (should be 0 for new task)
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
  });
});
