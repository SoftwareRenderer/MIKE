import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

// ============================================================
// CHAT FULLSCREEN — ESCAPE BEHAVIOR
// ============================================================
// After enabling chat fullscreen via the chat-sticky-fullscreen button,
// pressing Escape must exit fullscreen instead of closing the task.

test.describe.serial('Chat fullscreen Escape behavior', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('Escape exits chat fullscreen instead of closing the task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-fs-${Date.now()}`);

    // Seed enough user messages to make the chat scrollable so the sticky
    // header (which hosts the fullscreen toggle) appears on scroll.
    for (let i = 0; i < 4; i++) {
      const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        body: { role: 'user', content: `message ${i} ${'padding '.repeat(60)}` },
      });
      expect(result.ok).toBe(true);
    }

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat messages to render
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Scroll the chat to the bottom so the sticky header (with the fullscreen
    // toggle button) appears.
    await page
      .locator('.chat-messages-wrapper .virtual-scroll, .chat-messages-wrapper .virtual-list')
      .first()
      .evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await expect(page.locator('.chat-sticky-fullscreen')).toBeVisible({ timeout: 5000 });

    // Enable chat fullscreen
    await page.locator('.chat-sticky-fullscreen').click();
    const detail = page.locator('.task-detail-page');
    await expect(detail).toHaveClass(/chat-fullscreen/);

    // Press Escape → exits fullscreen, task view stays open
    await page.keyboard.press('Escape');
    await expect(detail).not.toHaveClass(/chat-fullscreen/);
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });
});
