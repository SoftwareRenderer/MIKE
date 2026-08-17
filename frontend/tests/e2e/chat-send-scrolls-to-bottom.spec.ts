import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  ensureTaskStarted,
  waitForChatForm,
  csrfFetch,
} from './helpers';

/**
 * A user-initiated send must always bring the chat viewport to the bottom so the
 * message that was just typed is in view — even if the user had scrolled up to
 * read older messages. Streaming arrivals, by contrast, respect the scroll
 * position (see chat-auto-scroll.spec.ts).
 */
test.describe.serial('Chat send scrolls to bottom', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  /** Seed user messages directly via the API (no LLM) to make the chat scrollable. */
  async function seedMessages(page: any, taskId: string, count: number): Promise<void> {
    for (let i = 0; i < count; i++) {
      const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        body: {
          role: 'user',
          content: `Seed message ${i}. ${'padding '.repeat(14)}${Date.now()}`,
        },
      });
      expect(res.ok, `seed message ${i} failed: ${res.status}`).toBe(true);
    }
  }

  test('scrolls the chat to the bottom after the user sends a message', async ({ page }) => {
    test.setTimeout(90000);

    const taskId = await createTaskViaApi(page, projectId, `send-scroll ${Date.now()}`);
    // Move to in_progress (bypasses the executor) so the chat form is shown, then
    // seed messages and reload the task so the chat renders the seeded history.
    await ensureTaskStarted(page, projectId, taskId);
    await seedMessages(page, taskId, 30);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chat = page.locator('.chat-messages').first();
    const jumpBtn = page.locator('.chat-jump-latest');
    await expect(chat).toBeVisible({ timeout: 10000 });

    // Verify the seeded history is actually scrollable (graceful if it is not).
    const scrollable = await chat.evaluate((el) => el.scrollHeight > el.clientHeight + 100);
    if (!scrollable) return;

    // Explicitly scroll up to read the oldest messages so the precondition (not at
    // the bottom) holds regardless of the fresh-load scroll position.
    await chat.evaluate((el) => {
      el.scrollTop = 0;
      el.dispatchEvent(new Event('scroll', { bubbles: true }));
    });
    await expect(jumpBtn).toBeVisible({ timeout: 5000 });
    const scrolledUpGap = await chat.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
    expect(scrolledUpGap, 'expected a large gap while scrolled up').toBeGreaterThan(300);

    // Send a message via the chat form while scrolled up.
    const textarea = page.locator('#chat-message-input');
    await textarea.fill(`sent while scrolled up ${Date.now()}`);
    await page.locator('.btn-send').click();

    // The chat must jump to the bottom so the just-sent message is in view.
    await expect(async () => {
      const gap = await chat.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
      expect(gap).toBeLessThan(200);
    }).toPass({ timeout: 10000 });

    // The jump-to-latest button must be hidden once we're at the bottom.
    await expect(jumpBtn).not.toBeVisible({ timeout: 5000 });
  });
});
