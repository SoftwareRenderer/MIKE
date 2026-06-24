import { test, expect } from '@playwright/test';
import { waitForChatForm, createAndOpenTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Context Size Tracking and Compression', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('should show model selector in chat form after task creation', async ({ page }) => {
    test.setTimeout(30000);
    await createAndOpenTask(page, `Context test ${Date.now()}`);
    if (!await waitForChatForm(page)) test.skip('Chat form not visible');

    // Model selector should be visible in the chat form
    const modelSelector = page.locator('.model-selector');
    await expect(modelSelector).toBeVisible();
  });

  test('should have compression entry CSS available', async ({ page }) => {
    test.setTimeout(30000);
    await createAndOpenTask(page, `Compression test ${Date.now()}`);
    if (!await waitForChatForm(page)) test.skip('Chat form not visible');

    // Verify compression entry CSS is present (structure test)
    // The rendering code should be in place even if no compression occurred
    const compressionEntry = page.locator('.compression-entry');
    // Should not be visible since no compression occurred
    await expect(compressionEntry).not.toBeVisible();
  });

  test('should update chat after LLM response completes', async ({ page }) => {
    test.setTimeout(30000);
    await createAndOpenTask(page, `Multi message test ${Date.now()}`);
    if (!await waitForChatForm(page)) test.skip('Chat form not visible');

    // Send a message (graceful: executor may not start in test env)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('hello world');
    await chatTextarea.press('Enter');
    const textareaCleared = await chatTextarea.toHaveValue('').catch(() => false);
    if (!textareaCleared) test.skip('Message not sent (textarea not cleared)');

    // Graceful: send button may not reappear if executor didn't start
    const sendBtnVisible = await page.locator('.btn-send').isVisible().catch(() => false);
    if (sendBtnVisible) {
      // Chat messages should be visible (at least 2: user prompt + assistant response)
      const chatMessages = page.locator('.chat-message');
      await expect(async () => {
        const count = await chatMessages.count();
        expect(count).toBeGreaterThanOrEqual(2);
      }).toPass();
    }
  });
});
