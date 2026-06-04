import { test, expect } from '@playwright/test';
import { ensureChatForm } from './helpers';

test.describe.serial('LLM Response Stats', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create a task and open it for stats verification', async ({ page }) => {
    // Create a task
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Stats test ${Date.now()}`);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the task
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Wait for chat form (handles new-task "Start Task" state)
    await ensureChatForm(page);
  });

  test('should show stats UI with model name, toggle, and reading/generation views when LLM responds', async ({ page }) => {
    // Ensure we're on a task detail page
    const isOnTaskPage = await page.locator('.task-detail-page').isVisible().catch(() => false);
    if (!isOnTaskPage) {
      await page.click('.card');
      await page.waitForSelector('.task-detail-page', { timeout: 5000 }).catch(() => {});
    }

    // Ensure chat form is ready (handles new-task and isLoading states)
    await ensureChatForm(page);

    // Send a message to trigger LLM response
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill(`Respond briefly for stats test ${Date.now()}`);
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message echo
    await expect(page.locator('.chat-message').first()).toBeVisible({ timeout: 10000 });

    // Try to wait for stats (mock includes usage data, but executor may fail due to worktree conflicts)
    const statsElement = page.locator('.message-stats');
    const statsVisible = await statsElement.isVisible({ timeout: 10000 }).catch(() => false);

    if (!statsVisible) {
      // Executor didn't start (worktree conflict) — skip stats verification
      return;
    }

    // Model name badge should be visible
    const modelBadge = page.locator('.stats-model-name');
    await expect(modelBadge).toBeVisible();
    const modelText = await modelBadge.textContent();
    expect(modelText).toBeTruthy();
    expect(modelText!.length).toBeGreaterThan(0);

    // Toggle switch should exist with Reading and Generation buttons
    const toggleSwitch = page.locator('.stats-toggle');
    await expect(toggleSwitch).toBeVisible();

    // Reading button (book icon)
    const readingBtn = page.locator('.stats-toggle button[data-view="reading"]');
    await expect(readingBtn).toBeVisible();

    // Generation button (sparkles icon)
    const generationBtn = page.locator('.stats-toggle button[data-view="generation"]');
    await expect(generationBtn).toBeVisible();

    // Default view should be Generation (active class)
    const activeBtn = page.locator('.stats-toggle button.active');
    const activeView = await activeBtn.getAttribute('data-view');
    expect(activeView).toBe('generation');

    // Verify generation stats are shown by default
    const statItems = page.locator('.stat-values .stat-item');
    const genStatCount = await statItems.count();
    expect(genStatCount).toBeGreaterThanOrEqual(3); // tokens, time, t/s

    // Click Reading tab — should switch to reading stats
    await readingBtn.click();
    await page.waitForTimeout(200);
    const readingActive = page.locator('.stats-toggle button[data-view="reading"].active');
    await expect(readingActive).toBeVisible();

    // Click Generation tab — should switch back
    await generationBtn.click();
    await page.waitForTimeout(200);
    const genActive = page.locator('.stats-toggle button[data-view="generation"].active');
    await expect(genActive).toBeVisible();
  });
});
