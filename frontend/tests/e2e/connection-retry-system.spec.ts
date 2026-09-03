import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForSendButton } from './helpers';

test.describe.serial('Connection retry renders as system message', () => {
  test('retry notification shows as a system card, not a user bubble', async ({ page }) => {
    test.setTimeout(90000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    // "fail-once-test" makes the mock LLM fail the first streaming request (500),
    // triggering the backend's "Connection interrupted ... retrying..." notification.
    const taskId = await createTaskViaApi(page, projectId, `fail-once-test prompt ${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);

    // Must appear as a system card, not a user bubble.
    const systemCard = page.locator('.system-card', { hasText: 'Connection interrupted' });
    await expect(systemCard).toBeVisible({ timeout: 30000 }); // 2s backoff + jitter before the hint
    await expect(systemCard.locator('.system-code')).toHaveText('System');
    // System cards are dismissible.
    await expect(systemCard.locator('[aria-label="Dismiss message"]')).toBeVisible();

    // Regression: never rendered as a user bubble.
    const userBubbles = page.locator('.chat-message.user .message-text', { hasText: 'Connection interrupted' });
    expect(await userBubbles.count()).toBe(0);

    // Retry recovers: the assistant response still streams in. `.last()` because a
    // successful retry appends to the assistant messages already on screen.
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible({ timeout: 60000 });
    await waitForSendButton(page);
  });
});
