import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

// ============================================================
// CHAT STICKY USER MESSAGE — sticky header correctness
// ============================================================
// The sticky header at the top of Task Chat shows the first line of the user
// message that is currently above the viewport. It must track the *rendered*
// positions (not estimates of message heights): the correct message is shown
// at each scroll position, and clicking the bubble scrolls to that exact
// message even when several user messages share the same first line.

const CHAT = '.chat-messages-wrapper .chat-messages';

/** Scroll the chat container to a content-space offset. */
async function setChatScrollTop(page: any, scrollTop: number) {
    await page.locator(CHAT).evaluate((el: HTMLElement, top: number) => {
        el.scrollTop = top;
        el.dispatchEvent(new Event('scroll'));
    }, scrollTop);
}

/** Content-space geometry of every rendered user message, in DOM order. */
async function userMsgGeometry(page: any): Promise<Array<{ entryIdx: number; firstLine: string; top: number; bottom: number }>> {
    return page.locator(CHAT).evaluate((el: HTMLElement) => {
        const cTop = el.getBoundingClientRect().top;
        return Array.from(el.querySelectorAll<HTMLElement>('.sticky-user-message')).map((n) => {
            const r = n.getBoundingClientRect();
            return {
                entryIdx: Number(n.dataset.entryIdx),
                firstLine: n.dataset.userMsgFirstLine ?? '',
                top: r.top - cTop + el.scrollTop,
                bottom: r.bottom - cTop + el.scrollTop,
            };
        });
    });
}

/**
 * Progressively sweep the chat from the top to the bottom so
 * `content-visibility: auto` renders every entry and remembers its real
 * size. Without this, off-screen entries report placeholder heights and the
 * measured geometry would be compressed. Starts from the top because the
 * history load auto-pins the viewport to the bottom.
 */
async function sweepRender(page: any) {
    await page.locator(CHAT).evaluate(async (el: HTMLElement) => {
        el.scrollTop = 0;
        const step = Math.max(50, Math.floor(el.clientHeight / 2));
        for (let i = 0; i < 120; i++) {
            el.scrollTop = Math.min(el.scrollTop + step, el.scrollHeight);
            el.dispatchEvent(new Event('scroll'));
            await new Promise((r) => requestAnimationFrame(r));
            await new Promise((r) => requestAnimationFrame(r));
            if (el.scrollTop >= el.scrollHeight - el.clientHeight - 2) break;
        }
        el.scrollTop = 0;
        el.dispatchEvent(new Event('scroll'));
        await new Promise((r) => requestAnimationFrame(r));
        await new Promise((r) => requestAnimationFrame(r));
    });
}

async function seedUserMessage(page: any, projectId: string, taskId: string, content: string) {
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
        method: 'POST',
        body: { role: 'user', content },
    });
    expect(result.ok).toBe(true);
}

test.describe.serial('Chat sticky user message', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('shows the user message that is actually above the viewport', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `sticky-${Date.now()}`);

    // Five tall user messages with distinct first lines. The line counts vary
    // on purpose so naive per-line height estimates diverge from the real
    // rendered layout; the last is taller than the chat viewport so that at
    // max scroll its top edge has crossed the viewport top.
    const lineCounts = [10, 25, 5, 40, 60];
    for (let i = 0; i < lineCounts.length; i++) {
      await seedUserMessage(
        page, projectId, taskId,
        `sticky probe ${i}\n` + 'lorem ipsum dolor sit amet consectetur\n'.repeat(lineCounts[i]),
      );
    }

    await navigateToTask(page, taskId);
    await expect(page.locator(CHAT)).toBeVisible({ timeout: 10000 });
    // The task prompt renders as the first user message; wait for the five
    // seeded probes (entry indices 1..5) and ignore the prompt in geometry.
    await expect.poll(async () => {
      const geo = await userMsgGeometry(page);
      return geo.filter((g) => g.firstLine.startsWith('sticky probe')).length;
    }, { timeout: 10000 }).toBe(5);

    await sweepRender(page);
    const geo = (await userMsgGeometry(page)).filter((g) => g.firstLine.startsWith('sticky probe'));
    expect(geo.map((g) => g.firstLine)).toEqual([
      'sticky probe 0', 'sticky probe 1', 'sticky probe 2', 'sticky probe 3', 'sticky probe 4',
    ]);

    const bubble = page.locator('.chat-sticky-bubble');

    // 1. First message still in view → no sticky header.
    await setChatScrollTop(page, 0);
    await expect(page.locator('.chat-sticky-header')).toHaveCount(0);

    // 2. Just past the bottom of message 1 → header shows message 1.
    await setChatScrollTop(page, geo[0].bottom + 5);
    await expect(bubble).toBeVisible({ timeout: 5000 });
    await expect(bubble).toHaveText('sticky probe 0');

    // 3. Messages 1–3 above the fold (message 4 not yet) → header shows 3.
    await setChatScrollTop(page, geo[2].top + 10);
    await expect(bubble).toHaveText('sticky probe 2');

    // 4. At the bottom → header shows the last message.
    await setChatScrollTop(page, 1e9); // browser clamps to the maximum
    await expect(bubble).toHaveText('sticky probe 4');
  });

  test('clicking the bubble scrolls to the exact message, even with duplicate first lines', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `sticky-dup-${Date.now()}`);

    // Three user messages sharing the same first line: tall, short, tall.
    const tall = '\n' + 'padding line that makes the message tall\n'.repeat(30);
    await seedUserMessage(page, projectId, taskId, `dup probe${tall}`);
    await seedUserMessage(page, projectId, taskId, `dup probe\nshort body`);
    await seedUserMessage(page, projectId, taskId, `dup probe${tall}`);

    await navigateToTask(page, taskId);
    await expect(page.locator(CHAT)).toBeVisible({ timeout: 10000 });
    // The task prompt renders as the first user message; wait for the three
    // seeded probes (entry indices 1..3) and ignore the prompt in geometry.
    await expect.poll(async () => {
      const geo = await userMsgGeometry(page);
      return geo.filter((g) => g.firstLine === 'dup probe').length;
    }, { timeout: 10000 }).toBe(3);

    await sweepRender(page);
    const geo = (await userMsgGeometry(page)).filter((g) => g.firstLine === 'dup probe');

    // The first two probes above the fold, the third not yet: the header
    // shows "dup probe" and must refer to the MIDDLE occurrence (the one
    // actually above the fold) — not the later duplicate.
    await setChatScrollTop(page, geo[1].bottom + 5);
    const bubble = page.locator('.chat-sticky-bubble');
    await expect(bubble).toBeVisible({ timeout: 5000 });
    await expect(bubble).toHaveText('dup probe');

    await bubble.click();
    // Smooth-scroll settles with the middle duplicate at the top of the chat
    // (within one entry margin) and none of the other duplicates there.
    await expect.poll(async () => {
      return page.locator(CHAT).evaluate((el: HTMLElement) => {
        const cTop = el.getBoundingClientRect().top;
        const nodes = Array.from(el.querySelectorAll<HTMLElement>('.sticky-user-message'))
          .filter((n) => n.dataset.userMsgFirstLine === 'dup probe');
        const top = (n: HTMLElement) => Math.abs(n.getBoundingClientRect().top - cTop);
        return {
          firstAtTop: top(nodes[0]) < 48,
          middleAtTop: top(nodes[1]) < 48,
          lastAtTop: top(nodes[2]) < 48,
        };
      });
    }, { timeout: 5000 }).toEqual({ firstAtTop: false, middleAtTop: true, lastAtTop: false });
  });
});
