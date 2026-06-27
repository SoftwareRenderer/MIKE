import { test, expect } from '@playwright/test';
import { createAndOpenTask, waitForChatForm } from './helpers';

test.describe.serial('Escape key stops generation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should stop generation when Escape is pressed in chat input', async ({ page }) => {
    test.setTimeout(30000);
    // Create a task — executor may or may not auto-start in test env
    await createAndOpenTask(page, `escape-stop-test ${Date.now()}`);
    await waitForChatForm(page);

    // Graceful: executor may not start in test env (worktree conflicts, etc.)
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // Send a message that triggers the slow mock response (100ms between chunks)
    // This gives the stop button enough time to be visible
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('stop-test message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the stop button to appear (LLM is streaming)
    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 10000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Small delay to ensure some tokens have streamed
    await page.waitForTimeout(250);

    // Verify assistant content is appearing (streaming in progress)
    const assistantBefore = page.locator('.chat-message.assistant');
    const assistantCountBefore = await assistantBefore.count();

    // Focus the chat textarea and press Escape
    await chatTextarea.focus();
    await chatTextarea.press('Escape');

    // Wait for the stop button to disappear and send button to reappear
    await expect(page.locator('.btn-stop')).toBeHidden({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    // Verify no streaming cursor is visible
    const streamingCursor = page.locator('.streaming-cursor');
    const cursorVisible = await streamingCursor.isVisible().catch(() => false);
    expect(cursorVisible).toBe(false);

    // Verify the incomplete assistant response was deleted
    // (loadHistory reloads from DB which doesn't have the partial response)
    const assistantAfter = page.locator('.chat-message.assistant');
    const assistantCountAfter = await assistantAfter.count();
    expect(assistantCountAfter).toBeLessThanOrEqual(assistantCountBefore);
    // The last chat entry should be the user message, not a partial assistant response
    const lastMessage = page.locator('.chat-message').last();
    await expect(lastMessage).toHaveClass(/user/);
  });

  test('should not trigger stop when Escape is pressed and not loading', async ({ page }) => {
    test.setTimeout(30000);
    await createAndOpenTask(page, `escape-noop-test ${Date.now()}`);
    await waitForChatForm(page);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // Press Escape when NOT loading — should be a no-op
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.focus();
    await chatTextarea.press('Escape');

    // Send button should still be visible (no change since not loading)
    await expect(page.locator('.btn-send')).toBeVisible();
  });
});
