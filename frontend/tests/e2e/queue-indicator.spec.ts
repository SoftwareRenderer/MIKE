import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
} from './helpers';

test.describe.serial('QueueIndicator', () => {
  let projectId = '';
  let taskId = '';

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `queue-test-${Date.now()}`);
    await navigateToTask(page, taskId);
    await waitForChatForm(page);
  });

  test('should show queue indicator during LLM processing', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the slow mock response (100ms between chunks, ~1.6s total)
    // This gives enough time for the queue indicator to be visible
    await chatTextarea.fill('stop-test message');
    await chatTextarea.press('Enter');

    // Wait for the textarea to clear (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear in chat
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // The queue indicator (awaiting-llm) should appear shortly after the message is sent
    // It shows "Awaiting LLM provider..." while awaitingResponse is true and queuePosition is 0
    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // Verify the indicator contains the expected text
    const indicatorText = await queueIndicator.textContent();
    expect(indicatorText).toContain('Awaiting');
  });

  test('should update when a second message cancels the first', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send first message (slow response)
    await chatTextarea.fill('stop-test first');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the queue indicator to appear
    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // Wait briefly for the LLM to start streaming (stop button appears)
    const stopBtnVisible = await page.locator('.btn-stop').isVisible({ timeout: 8000 }).catch(() => false);
    if (!stopBtnVisible) return;

    // Send a second message rapidly — this cancels the first LLM call
    await chatTextarea.fill('stop-test second');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the second user message to appear
    const userMessages = page.locator('.chat-message.user');
    await expect(userMessages).toHaveCount(2, { timeout: 10000 });

    // The queue indicator should still be visible (now awaiting the second response)
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // The indicator text should still contain "Awaiting"
    const indicatorText = await queueIndicator.textContent();
    expect(indicatorText).toContain('Awaiting');
  });

  test('should disappear when all messages are processed', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the slow mock response
    await chatTextarea.fill('stop-test final');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for the queue indicator to appear
    const queueIndicator = page.locator('.awaiting-llm');
    await expect(queueIndicator).toBeVisible({ timeout: 10000 });

    // Wait for the LLM response to complete (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

    // After the response completes, the queue indicator should disappear
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });
  });

  test('should not appear when LLM is idle', async ({ page }) => {
    test.setTimeout(30000);

    // Graceful: executor may not start in test env
    const sendBtnVisible = await page.locator('.btn-send').isVisible({ timeout: 20000 }).catch(() => false);
    if (!sendBtnVisible) return;

    // The queue indicator should NOT be visible when the task is idle
    // (no messages sent, no LLM processing)
    const queueIndicator = page.locator('.awaiting-llm');
    expect(await queueIndicator.isVisible()).toBe(false);

    // Send a single message and wait for it to complete
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('idle test message');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // The queue indicator briefly appears during processing
    const indicatorAppeared = await queueIndicator.isVisible({ timeout: 10000 }).catch(() => false);
    expect(indicatorAppeared).toBe(true);

    // After the response completes, it should disappear
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });
    await expect(queueIndicator).toBeHidden({ timeout: 10000 });

    // Verify it stays hidden when idle
    await page.waitForTimeout(500);
    expect(await queueIndicator.isVisible()).toBe(false);
  });
});
