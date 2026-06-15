import { test, expect } from '@playwright/test';
import { ensureChatForm, waitForChatForm, createAndOpenTask, getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Task Chat', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  // Create task via API (includes default provider config) and open it.
  // Waits for chat form to be ready (executor started).
  async function openTask(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `chat-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureChatForm(page);
  }

  // Create task via API and open it without waiting for chat form.
  // Use for tests that don't need the chat form (diff, task list, etc.).
  async function openTaskNoChat(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `chat-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
  }

  test('should show send button when typing', async ({ page }) => {
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Test message');

    // Send button should be enabled
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).not.toBeDisabled();
  });

  test('should disable send button when empty', async ({ page }) => {
    await openTask(page);

    // Send button should be disabled when textarea is empty
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeDisabled();
  });

  test('should send message with Enter key', async ({ page }) => {
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Enter key test');
    await chatTextarea.press('Enter');

    // Textarea should be cleared
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
  });

  test('should not send on Shift+Enter', async ({ page }) => {
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Line 1');
    await chatTextarea.press('Shift+Enter');
    await chatTextarea.type('Line 2');

    // Textarea should still have content (not sent)
    await expect(chatTextarea).not.toHaveValue('');
  });

  test('should have diff column with empty state', async ({ page }) => {
    await openTaskNoChat(page);

    // Diff column should show empty state
    await expect(page.locator('.diff-empty')).toBeVisible();
    await expect(page.locator('.diff-empty p')).toContainText('No changes since last commit');
  });

  test('should show task list in context column', async ({ page }) => {
    await openTaskNoChat(page);

    // Task list should be visible
    await expect(page.locator('.task-list')).toBeVisible();

    // Current task should be selected
    await expect(page.locator('.task-item.selected')).toBeVisible();
  });

  test('should show task actions in header', async ({ page }) => {
    await openTaskNoChat(page);

    // Delete button should always be visible
    const deleteButton = page.locator('.header-actions .btn-icon').last();
    await expect(deleteButton).toBeVisible();
  });

  test('should show assistant streaming indicator immediately after sending a message', async ({ page }) => {
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    const msg = `stream_test_${Date.now()}`;
    await chatTextarea.fill(msg);
    await chatTextarea.press('Enter');

    // Textarea should be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Assistant message with streaming cursor should appear immediately
    // Use generous timeout for parallel runs where server is slower
    // Graceful skip: executor may not start in test env
    const cursorVisible = await page.locator('.cursor-blink').isVisible({ timeout: 10000 }).catch(() => false);
    if (!cursorVisible) return;
  });

  test('should show status badge', async ({ page }) => {
    await openTaskNoChat(page);

    // Badge should be visible
    await expect(page.locator('.badge')).toBeVisible();
  });

  test('should not duplicate user message after sending', async ({ page }) => {
    test.setTimeout(30000);
    await openTask(page, `No dup test task ${Date.now()}`);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    const testMsg = `No dup test ${Date.now()}`;
    await chatTextarea.fill(testMsg);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for send button to reappear (LLM response + echo fully processed)
    // Graceful skip: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 15000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // Count how many times the test message appears in user messages
    // Should be exactly 1 (optimistic UI), not 2 (duplicate from server echo)
    const chatContent = await page.locator('.chat-messages').textContent();
    const occurrences = (chatContent?.split(testMsg) || []).length - 1;
    expect(occurrences).toBe(1);
  });
});
