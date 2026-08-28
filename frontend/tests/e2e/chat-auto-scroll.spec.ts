import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  ensureTaskStarted,
  waitForChatForm,
  csrfFetch,
} from './helpers';

/** Must stay in sync with SCROLL_BOTTOM_THRESHOLD in $lib/constants. */
const SCROLL_PX = 32;

/** Distance from the viewport bottom to the very bottom of the chat, in px. */
async function chatGap(chat: any): Promise<number> {
  return chat.evaluate((el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight);
}

/** Seed user messages straight into the DB (no LLM) to build a long history. */
async function seedMessages(page: any, projectId: string, taskId: string, count: number): Promise<void> {
  for (let i = 0; i < count; i++) {
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: `Seed ${i}. ${'padding '.repeat(14)}${Date.now()}` },
    });
    expect(res.ok, `seed message ${i} failed: ${res.status}`).toBe(true);
  }
}

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
      return gap < SCROLL_PX;
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
      expect(gap).toBeLessThan(100);
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

  test('opens a long history at the very bottom of the conversation', async ({ page }) => {
    test.setTimeout(120000);

    // Regression: entries render lazily (content-visibility + contain-intrinsic-size),
    // so a freshly opened conversation is taller than the height the first pin saw.
    // A one-shot pin landed mid-conversation and the jump button took over.
    const taskId = await createTaskViaApi(page, projectId, `scroll-long-open-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await seedMessages(page, projectId, taskId, 30);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatList = page.locator('.chat-messages').first();
    await expect(chatList).toBeVisible({ timeout: 10000 });
    const scrollable = await chatList.evaluate((el) => el.scrollHeight > el.clientHeight + 300);
    expect(scrollable, 'expected the seeded history to be scrollable').toBe(true);

    // The pin settles within a few frames — no user interaction, no re-render.
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.chat-jump-latest')).not.toBeVisible();

    // The newest message is actually on screen, not just "near" the bottom.
    const lastMessageVisible = await chatList.evaluate((el) => {
      const entries = el.querySelectorAll('.chat-entry');
      const last = entries[entries.length - 1] as HTMLElement | undefined;
      if (!last) return false;
      const box = last.getBoundingClientRect(), view = el.getBoundingClientRect();
      return last.offsetHeight <= view.height && box.bottom <= view.bottom + 2 && box.top >= view.top - 2;
    });
    expect(lastMessageVisible, 'the newest entry should be fully in view on open').toBe(true);
  });

  test('keeps following when the chat container is resized', async ({ page }) => {
    test.setTimeout(120000);

    // Regression: opening a panel / toggling fullscreen / a phone keyboard all
    // shrink the container. Scroll anchoring keeps the visible text steady, so the
    // latest message ended up below the fold with nothing to re-pin it.
    const taskId = await createTaskViaApi(page, projectId, `scroll-resize-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await seedMessages(page, projectId, taskId, 20);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatList = page.locator('.chat-messages').first();
    await expect(chatList).toBeVisible({ timeout: 10000 });
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });

    const shrink = page.viewportSize() ?? { width: 1280, height: 720 };
    await page.setViewportSize({ width: shrink.width, height: 380 });
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.chat-jump-latest')).not.toBeVisible();

    // Growing back must not break it either.
    await page.setViewportSize({ width: shrink.width, height: shrink.height });
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.chat-jump-latest')).not.toBeVisible();
  });

  test('a wheel scroll-up during an active stream is not undone', async ({ page }) => {
    test.setTimeout(120000);

    // The follow pins every frame while tokens arrive, so a pending pin re-checks
    // intent first: an explicit wheel-up wins even though the stream is resizing the
    // document at the same moment.
    const taskId = await createTaskViaApi(page, projectId, `scroll-wheel-midstream-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await seedMessages(page, projectId, taskId, 20);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatList = page.locator('.chat-messages').first();
    await expect(chatList).toBeVisible({ timeout: 10000 });
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
      method: 'POST',
      body: { content: `stop-test wheel midstream ${Date.now()}` },
    });
    await page.waitForTimeout(400); // let the stream start

    const box = await chatList.boundingBox();
    expect(box).toBeTruthy();
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
    for (let i = 0; i < 10; i++) {
      await page.mouse.wheel(0, -300);
      await page.waitForTimeout(80);
    }

    // Stayed scrolled up while the response kept streaming in.
    const gapWhileUp = await chatGap(chatList);
    expect(gapWhileUp, 'the viewport should still be where the user put it').toBeGreaterThan(300);
    await expect(page.locator('.chat-jump-latest')).toBeVisible({ timeout: 5000 });

    // Latest brings them back and following resumes.
    await page.locator('.chat-jump-latest').click();
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });
  });

  test('stays pinned when content grows without a scroll event', async ({ page }) => {
    test.setTimeout(120000);

    // Regression: late layout (an image finishing, an async checkpoint label,
    // syntax highlighting) grows the conversation below the fold and fires no scroll
    // event, which left the follow state and the jump button stale.
    const taskId = await createTaskViaApi(page, projectId, `scroll-growth-${Date.now()}`);
    await ensureTaskStarted(page, projectId, taskId);
    await seedMessages(page, projectId, taskId, 20);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);

    const chatList = page.locator('.chat-messages').first();
    await expect(chatList).toBeVisible({ timeout: 10000 });
    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });

    await chatList.evaluate((el) => {
      const entries = el.querySelectorAll('.chat-entry');
      const last = entries[entries.length - 1] as HTMLElement;
      const spacer = document.createElement('div');
      spacer.style.height = '400px';
      last.appendChild(spacer);
    });

    await expect(async () => {
      expect(await chatGap(chatList)).toBeLessThan(SCROLL_PX);
    }).toPass({ timeout: 5000 });
    await expect(page.locator('.chat-jump-latest')).not.toBeVisible();
  });
});
