import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, waitForChatForm } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Cancel During Streaming', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should cancel LLM response when user sends new message during streaming', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `cancel-test-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat form to be present (task may be in backlog, executor not yet started).
    await waitForChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers a slow LLM response (100ms between chunks).
    // This triggers lazy-start — the executor starts and begins streaming.
    await chatTextarea.fill('cancel-test slow response');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for streaming to start (stop button appears — from the cancelled response or the new one).
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });

    // Record the number of chat messages before sending the second message.
    const messagesBefore = await page.locator('.chat-message').count();

    // Type and send a new message — this should cancel the streaming response.
    await chatTextarea.fill('Second message after cancel');
    // Send button should appear when typing during streaming.
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 5000 });
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent).
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Verify the second user message appears in chat.
    await expect(page.locator('.chat-message').filter({ hasText: 'Second message after cancel' }))
      .toBeVisible({ timeout: 10000 });

    // Verify new messages were added (user message + new assistant response).
    await expect(async () => {
      const count = await page.locator('.chat-message').count();
      expect(count).toBeGreaterThan(messagesBefore);
    }).toPass({ timeout: 10000 });

    // Wait for the new response to complete (send button reappears).
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });

  test('should show send button when typing during streaming', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `cancel-test-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Start a slow streaming response.
    await chatTextarea.fill('cancel-test streaming');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for stop button (streaming active).
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });

    // Send button should NOT be visible when input is empty.
    await expect(page.locator('.btn-send')).not.toBeVisible();

    // Type text — send button should replace stop button.
    await chatTextarea.fill('New message');
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.btn-stop')).not.toBeVisible();

    // Clear text — stop button should reappear.
    await chatTextarea.fill('');
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.btn-send')).not.toBeVisible();

    // Clean up: stop the streaming response (if still streaming).
    try {
        await page.locator('.btn-stop').click({ timeout: 3000 });
    } catch {
        // Response may have completed — stop button no longer exists
    }
  });

  test('should process multiple rapid messages during streaming', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `cancel-test-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Start a slow streaming response.
    await chatTextarea.fill('cancel-test multi message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for streaming to start.
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 15000 });

    // Send two messages rapidly.
    await chatTextarea.fill('First follow-up');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    await chatTextarea.fill('Second follow-up');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Both follow-up messages should appear in chat.
    await expect(page.locator('.chat-message').filter({ hasText: 'First follow-up' }))
      .toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message').filter({ hasText: 'Second follow-up' }))
      .toBeVisible({ timeout: 10000 });

    // Wait for the response to complete.
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  });
});
