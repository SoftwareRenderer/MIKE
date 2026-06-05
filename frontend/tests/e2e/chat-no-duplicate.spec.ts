import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask } from './helpers';

test.describe('Chat Message Deduplication', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should not duplicate user message after sending', async ({ page }) => {
    // Create a fresh task to avoid state pollution from other tests
    await createAndOpenTask(page, `No dup test task ${Date.now()}`);

    // Ensure chat form is ready (handles new-task "Start Task" state)
    await ensureChatForm(page);

    // Send a message
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const testMsg = `No dup test ${Date.now()}`;
    await chatTextarea.fill(testMsg);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for send button to reappear (LLM response + echo fully processed)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Count how many times the test message appears in user messages
    // Should be exactly 1 (optimistic UI), not 2 (duplicate from server echo)
    const chatContent = await page.locator('.chat-messages').textContent();
    const occurrences = (chatContent?.split(testMsg) || []).length - 1;
    expect(occurrences).toBe(1);
  });
});
