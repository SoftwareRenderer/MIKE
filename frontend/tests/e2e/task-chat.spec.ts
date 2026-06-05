import { test, expect } from '@playwright/test';
import { ensureChatForm, waitForChatForm, createAndOpenTask } from './helpers';

test.describe('Task Chat', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show send button when typing', async ({ page }) => {
    await createAndOpenTask(page);
    await ensureChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Test message');

    // Send button should be enabled
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).not.toBeDisabled();
  });

  test('should disable send button when empty', async ({ page }) => {
    await createAndOpenTask(page);
    await ensureChatForm(page);

    // Send button should be disabled when textarea is empty
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeDisabled();
  });

  test('should send message with Enter key', async ({ page }) => {
    await createAndOpenTask(page);
    await ensureChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Enter key test');
    await chatTextarea.press('Enter');

    // Textarea should be cleared
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
  });

  test('should not send on Shift+Enter', async ({ page }) => {
    await createAndOpenTask(page);
    await ensureChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Line 1');
    await chatTextarea.press('Shift+Enter');
    await chatTextarea.type('Line 2');

    // Textarea should still have content (not sent)
    await expect(chatTextarea).not.toHaveValue('');
  });

  test('should have diff column with refresh button', async ({ page }) => {
    await createAndOpenTask(page);

    // Diff column should show empty state
    await expect(page.locator('.diff-empty')).toBeVisible();
    await expect(page.locator('.diff-empty p')).toContainText('No changes yet');

    // Refresh button should be visible
    await expect(page.locator('.column-diff .btn-icon-sm')).toBeVisible();
  });

  test('should show task list in context column', async ({ page }) => {
    await createAndOpenTask(page);

    // Task list should be visible
    await expect(page.locator('.task-list')).toBeVisible();

    // Current task should be selected
    await expect(page.locator('.task-item.selected')).toBeVisible();
  });

  test('should show task actions in header', async ({ page }) => {
    await createAndOpenTask(page);

    // Delete button should always be visible
    const deleteButton = page.locator('.header-actions .btn-icon').last();
    await expect(deleteButton).toBeVisible();
  });

  test('should allow editing task title', async ({ page }) => {
    await createAndOpenTask(page);

    // Click on title to edit
    await page.click('.title');

    // Title input should appear
    await expect(page.locator('.title-input')).toBeVisible();
  });

  test('should show assistant streaming indicator immediately after sending a message', async ({ page }) => {
    await createAndOpenTask(page);
    await ensureChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    const msg = `stream_test_${Date.now()}`;
    await chatTextarea.fill(msg);
    await chatTextarea.press('Enter');

    // Textarea should be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Assistant message with streaming cursor should appear immediately
    // Use a tight timeout (500ms) to verify it's created on send,
    // not waiting for the first LLM token
    await expect(page.locator('.cursor-blink')).toBeVisible({ timeout: 500 });
  });

  test('should show status badge', async ({ page }) => {
    await createAndOpenTask(page);

    // Badge should be visible
    await expect(page.locator('.badge')).toBeVisible();
  });
});
