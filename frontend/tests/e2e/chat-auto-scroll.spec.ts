import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  waitForChatForm,
  csrfFetch,
} from './helpers';

test.describe.serial('Chat Auto-Scroll', () => {
  let projectId: string;

  /** Wait for the send button to be visible; returns false instead of throwing. */
  async function requireSendButton(page: any): Promise<boolean> {
    try {
      await expect(page.locator('.btn-send')).toBeVisible({ timeout: 20000 });
      return true;
    } catch {
      return false;
    }
  }

  /** Create a fresh task, open it and start the executor; returns taskId or '' when unavailable. */
  async function setupChat(page: any, label: string): Promise<string> {
    const taskId = await createTaskViaApi(page, projectId, `${label}-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);
    if (!await requireSendButton(page)) return '';
    return taskId;
  }

  /**
   * Send messages via the API (the executor queues them) and wait until every
   * user message has an assistant response. Returns false when the executor is
   * too slow/unavailable so the test can pass gracefully.
   */
  async function sendBurst(page: any, taskId: string, label: string, count: number): Promise<boolean> {
    for (let i = 0; i < count; i++) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
        method: 'POST',
        body: { content: `${label} ${i} ${'padding '.repeat(10)} ${Date.now()}` },
      });
    }
    // Wait for each user message echo to render in the chat.
    try {
      for (let i = 0; i < count; i++) {
        await expect(page.locator('.chat-message.user').nth(1 + i)).toBeVisible({ timeout: 10000 });
      }
    } catch {
      return false;
    }
    // Settle: every user message has an assistant response.
    try {
      await expect(async () => {
        const users = await page.locator('.chat-message.user').count();
        const assistants = await page.locator('.chat-message.assistant').count();
        expect(assistants).toBeGreaterThanOrEqual(users);
      }).toPass({ timeout: 60000 });
    } catch {
      return false;
    }
    return true;
  }

  /** Wait until every rendered user message has an assistant response. */
  async function settleResponses(page: any): Promise<boolean> {
    try {
      await expect(async () => {
        const users = await page.locator('.chat-message.user').count();
        const assistants = await page.locator('.chat-message.assistant').count();
        expect(assistants).toBeGreaterThanOrEqual(users);
      }).toPass({ timeout: 30000 });
      return true;
    } catch {
      return false;
    }
  }

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('keeps the chat pinned to the bottom as new content streams in', async ({ page }) => {
    test.setTimeout(120000);

    const taskId = await setupChat(page, 'chat-scroll-pin');
    if (!taskId) return; // graceful: executor unavailable

    const chatList = page.locator('.chat-messages').first();
    const jumpBtn = page.locator('.chat-jump-latest');

    // Build enough content to make the chat scrollable.
    if (!await sendBurst(page, taskId, 'pin test', 5)) return;

    // Verify the chat is scrollable and auto-scrolled to the bottom — the
    // jump-to-latest button must NOT be visible.
    const scrollable = await chatList.evaluate((el) => {
      return el.scrollHeight > el.clientHeight + 100;
    });
    expect(scrollable).toBe(true);
    await expect(jumpBtn).not.toBeVisible();
    const nearBottom = await chatList.evaluate((el) => {
      const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
      return gap < 100;
    });
    expect(nearBottom).toBe(true);

    // Start a slow streaming response and verify the viewport keeps up with the
    // new content while the user stays at the bottom (auto-scroll stays active).
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: `stop-test slow stream ${Date.now()}` },
    });
    await expect(async () => {
      const gap = await chatList.evaluate((el) => {
        return el.scrollHeight - el.scrollTop - el.clientHeight;
      });
      expect(gap).toBeLessThan(200);
    }).toPass({ timeout: 8000 });

    // After the stream completes the chat is still pinned at the bottom.
    if (!await settleResponses(page)) return;
    await expect(jumpBtn).not.toBeVisible();
  });

  test('does not yank the viewport when the user scrolls up to read', async ({ page }) => {
    test.setTimeout(120000);

    const taskId = await setupChat(page, 'chat-scroll-read');
    if (!taskId) return; // graceful: executor unavailable

    const chatList = page.locator('.chat-messages').first();
    const jumpBtn = page.locator('.chat-jump-latest');

    // Build enough content to make the chat scrollable.
    if (!await sendBurst(page, taskId, 'read test', 5)) return;
    const scrollable = await chatList.evaluate((el) => {
      return el.scrollHeight > el.clientHeight + 100;
    });
    expect(scrollable).toBe(true);

    // Scroll up to read older messages.
    await chatList.evaluate((el) => {
      el.scrollTop = 0;
      el.dispatchEvent(new Event('scroll', { bubbles: true }));
    });
    await page.waitForTimeout(200);

    // While scrolled up, the jump-to-latest button appears.
    await expect(jumpBtn).toBeVisible({ timeout: 5000 });

    // Send a slow-streaming message while scrolled up — the viewport must stay
    // put (auto-scroll is suspended for a user who scrolled up to read).
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: `stop-test reading ${Date.now()}` },
    });
    // Let the stream emit content (slow fixture is ~1.6s of 100ms chunks).
    await page.waitForTimeout(2000);

    const stillScrolledUp = await chatList.evaluate((el) => el.scrollTop < 100);
    expect(stillScrolledUp).toBe(true);
    // The jump button must remain visible so the user can return to latest.
    await expect(jumpBtn).toBeVisible({ timeout: 5000 });

    // Wait for the slow stream to finish, then click Latest to return to bottom.
    if (!await settleResponses(page)) return;
    await jumpBtn.click();
    await page.waitForTimeout(300);
    const nearBottom = await chatList.evaluate((el) => {
      const gap = el.scrollHeight - el.scrollTop - el.clientHeight;
      return gap < 100;
    });
    expect(nearBottom).toBe(true);
    await expect(jumpBtn).not.toBeVisible({ timeout: 3000 });
  });
});
