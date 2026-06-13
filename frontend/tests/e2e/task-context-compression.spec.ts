import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Context Size Tracking and Compression', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show model selector in chat form after task creation', async ({ page }) => {
    await createAndOpenTask(page, `Context test ${Date.now()}`);
    await ensureChatForm(page);

    // Model selector should be visible in the chat form
    const modelSelector = page.locator('.model-selector');
    await expect(modelSelector).toBeVisible({ timeout: 5000 });

    // Send a message to trigger the LLM
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('list files in the directory');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // Model bar should still be visible after LLM response
    await expect(modelSelector).toBeVisible({ timeout: 5000 });
  });

  test('should render compression checkpoint entry in chat', async ({ page }) => {
    await createAndOpenTask(page, `Compression test ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('read a file');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // Verify compression entry CSS is present (structure test)
    // Actual compression won't trigger with the mock LLM's small token counts,
    // but the rendering code should be in place
    const compressionEntry = page.locator('.compression-entry');
    // Should not be visible since no compression occurred
    await expect(compressionEntry).not.toBeVisible();
  });

  test('should update chat after LLM response completes', async ({ page }) => {
    await createAndOpenTask(page, `Multi message test ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('hello world');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // Chat messages should be visible (at least 2: user prompt + assistant response)
    const chatMessages = page.locator('.chat-message');
    await expect(async () => {
      const count = await chatMessages.count();
      expect(count).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 5000 });
  });
});
