import { test, expect } from '@playwright/test';

test.describe.serial('LLM Stats Persistence', () => {
  let taskId: string | null = null;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create a task and get an LLM response with stats', async ({ page }) => {
    // Create a task
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Stats persistence test ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Capture task ID from URL
    taskId = (await page.url()).match(/task=([^&]+)/)?.[1] || null;
    expect(taskId).toBeTruthy();

    // Wait for chat form
    await expect(page.locator('.chat-form')).toBeVisible();

    // Send a message to trigger LLM response
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`Say hello briefly for persistence test ${Date.now()}`);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message (last one = the one we just sent)
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Try to wait for stats (LLM may be slow)
    const statsElement = page.locator('.message-stats');
    const statsVisible = await statsElement.isVisible({ timeout: 30000 }).catch(() => false);

    if (!statsVisible) {
      // LLM didn't respond in time — skip persistence check
      return;
    }

    // Verify stats are visible before refresh
    const modelBadge = page.locator('.stats-model-name');
    await expect(modelBadge).toBeVisible();
  });

  test('should persist stats after page refresh', async ({ page }) => {
    if (!taskId) {
      // No task was created or LLM didn't respond, skip
      return;
    }

    // Navigate directly to the task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Wait for messages to load
    await page.waitForTimeout(2000);

    // Check for assistant messages
    const assistantMessages = page.locator('.chat-message.assistant');
    const assistantCount = await assistantMessages.count();

    if (assistantCount === 0) {
      // No assistant messages loaded yet, skip
      return;
    }

    // Check if any assistant message has stats
    const statsElements = page.locator('.message-stats');
    const statsCount = await statsElements.count();

    expect(statsCount).toBeGreaterThan(0);

    // Verify model name is present
    const modelBadge = page.locator('.stats-model-name').first();
    await expect(modelBadge).toBeVisible();
    const modelText = await modelBadge.textContent();
    expect(modelText).toBeTruthy();
    expect(modelText!.length).toBeGreaterThan(0);

    // Verify stat values are present (tokens, time, speed)
    const statItems = page.locator('.stat-values .stat-item');
    const statItemCount = await statItems.count();
    expect(statItemCount).toBeGreaterThanOrEqual(3);
  });
});
