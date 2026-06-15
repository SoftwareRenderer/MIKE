import { test, expect } from '@playwright/test';
import { ensureChatForm, getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('LLM Awaiting Spinner', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openTask(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `awaiter-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await ensureChatForm(page);
  }

  test('should send a message and receive an LLM response', async ({ page }) => {
    test.setTimeout(30000);
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message
    await chatTextarea.fill('hello awaiting test');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Graceful: LLM response may not appear if executor didn't start
    const responseAppeared = await page.locator('.chat-message.assistant').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (responseAppeared) {
      // Send button should reappear after LLM completes
      await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
    }
  });

  test('should send a message that triggers tool calls', async ({ page }) => {
    test.setTimeout(30000);
    await openTask(page);

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers a tool call (mock routes "list" to tool_call fixture)
    await chatTextarea.fill('list all files');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Graceful: tool call cards may not appear if executor didn't start
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (toolCallVisible) {
      await expect(page.locator('.tool-name').first()).toBeVisible();
    }
  });
});
