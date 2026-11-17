import { test, expect, type Page } from '@playwright/test';
import {
  apiGetJson, createTaskViaApi, navigateToTask, waitForChatForm, waitForTaskState, notInFlight,
  isolatedProject, getProjectId, gotoProject, seedChatRows,
} from './helpers';

isolatedProject();

// A transcript longer than one window, because the wake-up read is the interesting case: it answers the
// newest answers only, and the transcript draws that read ahead of every entry it does not name. A read
// that replaces the older pages the reader paged into therefore moves the newest answers to the top of the
// list and leaves an old row at the bottom — which is where the jump-to-latest button then lands.

const SCROLLER = '.chat-messages';
const ROWS = '.chat-entry[data-entry-id]';
const ROWS_PER_PAGE = 5;

/** What the server calls the newest answer: the first word of its row. */
async function newestRow(page: Page, projectId: string, taskId: string): Promise<string> {
  const window = await apiGetJson<any[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`);
  const last = window[window.length - 1];
  expect(last, 'the task has no rows to read').toBeTruthy();
  return String(last.content).trim().split(/\s+/)[0];
}

interface View {
  rows: number; scrollTop: number;
  /** The entry under the middle of the viewport, and how far below the top edge it sits. */
  anchor: string; anchorTop: number;
  first: string; last: string;
  /** True when the last entry has pixels inside the scroller. */
  lastInView: boolean;
}

async function view(page: Page): Promise<View> {
  return page.evaluate(([scroller, rows]) => {
    const el = document.querySelector(scroller) as HTMLElement;
    const box = el.getBoundingClientRect();
    const nodes = Array.from(el.querySelectorAll<HTMLElement>(rows));
    const mid = box.top + box.height / 2;
    const anchor = nodes.find(n => {
      const r = n.getBoundingClientRect();
      return r.top <= mid && r.bottom >= mid;
    });
    const last = nodes[nodes.length - 1]?.getBoundingClientRect();
    const tag = (t: string | undefined) => (t ?? '').trim().slice(0, 64);
    return {
      rows: nodes.length,
      scrollTop: Math.round(el.scrollTop),
      anchor: anchor?.dataset.entryId ?? '(none)',
      anchorTop: anchor ? Math.round(anchor.getBoundingClientRect().top - box.top) : 0,
      first: tag(nodes[0]?.textContent),
      last: tag(nodes[nodes.length - 1]?.textContent),
      lastInView: !!last && last.bottom > box.top && last.top < box.bottom,
    };
  }, [SCROLLER, ROWS]);
}

/** The transcript has stopped moving: four reads a beat apart see the same rows, the same anchor and the
 *  same offset. The read a wake-up issues paints over several frames, and a measurement taken in the middle
 *  of it samples nothing. */
async function settledView(page: Page): Promise<View> {
  let previous = await view(page);
  let repeats = 0;
  await expect.poll(async () => {
    const now = await view(page);
    const still = now.rows === previous.rows && now.scrollTop === previous.scrollTop
      && now.anchor === previous.anchor && now.anchorTop === previous.anchorTop
      && now.first === previous.first && now.last === previous.last;
    repeats = still ? repeats + 1 : 0;
    previous = now;
    return repeats;
  }, { timeout: 15000, intervals: [250] }).toBeGreaterThanOrEqual(3);
  return previous;
}

/** Scroll to the top edge until no older page arrives, which is what a reader paging up does. */
async function pageToTheTop(page: Page): Promise<number> {
  let pages = 0;
  for (;;) {
    const before = (await view(page)).rows;
    await page.evaluate((s) => { (document.querySelector(s) as HTMLElement).scrollTop = 0; }, SCROLLER);
    const grew = await expect(async () => {
      expect((await view(page)).rows).toBeGreaterThan(before);
    }).toPass({ timeout: 5000 }).then(() => true).catch(() => false);
    if (!grew) return pages;
    pages++;
  }
}

test.describe('returning to a tab whose reader had scrolled up', () => {
  test('the newest message stays at the bottom of the transcript', async ({ page }) => {
    test.setTimeout(120000);
    await gotoProject(page);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `tab-return-scroll ${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(ROWS).first()).toBeVisible({ timeout: 15000 });
    // A task starts a turn when it is created; let it finish, so what the server calls newest stays newest
    // while the rows below are written.
    await waitForTaskState(page, projectId, taskId, notInFlight, 60000);
    await seedChatRows(page, projectId, taskId, ROWS_PER_PAGE * 4, 'tab-return');
    await page.reload();
    await waitForChatForm(page);
    await expect(page.locator(ROWS).first()).toBeVisible({ timeout: 15000 });
    const newest = await newestRow(page, projectId, taskId);

    const pages = await pageToTheTop(page);
    expect(pages, 'no older page arrived, so this run cannot show what a read replaces').toBeGreaterThan(1);
    // Sit somewhere in the middle of what is loaded, like a reader who stopped scrolling.
    await page.evaluate((s) => {
      const el = document.querySelector(s) as HTMLElement;
      el.scrollTop = Math.round(el.scrollHeight / 3);
    }, SCROLLER);
    await expect(async () => { expect((await view(page)).anchor).not.toBe('(none)'); }).toPass();
    const before = await settledView(page);
    expect(before.last, 'the newest message is not at the bottom before the switch').toContain(newest);

    // The browser fires this at `document`, where it bubbles as far as `window`.
    const read = page.waitForResponse(r => r.url().includes('/messages') && !r.url().includes('before='));
    await page.evaluate(() => { document.dispatchEvent(new Event('visibilitychange', { bubbles: true })); });
    await read;

    const after = await settledView(page);
    expect(after.last, 'the read took the newest message off the bottom of the transcript').toContain(newest);
    expect(after.first, 'the read reordered what the reader had paged into').toBe(before.first);
    expect(after.anchor, 'the reader lost the row they were reading').toBe(before.anchor);
    expect(Math.abs(after.anchorTop - before.anchorTop), 'the reader was moved off their row').toBeLessThan(40);

    await page.locator('.chat-jump-latest').click();
    await expect(async () => {
      const landed = await view(page);
      expect(landed.lastInView, 'the jump to latest landed on content that is not the newest').toBe(true);
      expect(landed.last, 'the jump to latest did not land on the newest message').toContain(newest);
    }).toPass({ timeout: 10000 });
  });
});
