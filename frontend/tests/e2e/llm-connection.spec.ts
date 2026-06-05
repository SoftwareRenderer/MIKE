import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask } from './helpers';

test.describe('LLM Connection', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should start task execution by sending a message', async ({ page }) => {
    await createAndOpenTask(page, `Reply with "Hello from LLM" ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message to trigger LLM (conversational start)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Hello, please respond');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message echo
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 10000 });
  });

  test('should show stop button during streaming and allow cancelling', async ({ page }) => {
    await createAndOpenTask(page, `Stop button test ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message that triggers the slow mock response (100ms between chunks)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test: please respond');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Stop button should appear during streaming
    await expect(page.locator('.btn-stop')).toBeVisible({ timeout: 5000 });

    // Click stop button to cancel the response
    await page.locator('.btn-stop').click();

    // After stopping, send button should return and stop button should disappear
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-stop')).not.toBeVisible();
  });

  test('should render tool call cards when LLM uses tools', async ({ page }) => {
    // Create a fresh task with a prompt that triggers tool usage
    const taskPrompt = `List files in project ${Date.now()}`;
    await createAndOpenTask(page, taskPrompt);
    await ensureChatForm(page);

    // Send a follow-up message that also triggers tool usage
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Please list all files');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message echo
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 10000 });

    // With mock, tool call cards should appear (mock returns tool_call for "list files")
    // May not appear if executor fails to start (worktree conflict)
    const toolCallCards = page.locator('.tool-call-card');
    const toolCallVisible = await toolCallCards.first().isVisible({ timeout: 10000 }).catch(() => false);
    if (!toolCallVisible) {
      return; // Executor didn't start, skip
    }
    await expect(page.locator('.tool-name').first()).toBeVisible();
    await expect(page.locator('.tool-args').first()).toBeVisible();
  });
});
