import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, ownProjectId, createTaskViaApi, ensureTaskStarted, navigateToTask,
  waitForTaskState, waitForReaction,
} from './helpers';

isolatedProject();

/**
 * Opening a task this browser already saw ending in Review: its own cached rows go on screen while the
 * newest-window read is still out, so a returning reader reads the answer they left behind instead of a
 * spinner. Only a window ending on a `request_review` result is drawn this way — any other newest row is
 * work that can still move — and the answered read replaces the rows the moment it lands.
 * `docs/reconnection.md → The tab that comes back`
 */

const TRANSCRIPT = '.chat-messages';
const PENDING = '.chat-pending';
const BAND = '.history-notice';
const CARD = '.tool-call-card';
const REVIEW = '.tool-call-card .tool-name:text-is("Done")';

/** The turn that ends in Review: one `request_review` call and nothing after it. */
const REVIEW_PROMPT = 'llm-fixture:tool_call_complete';
const notInFlight = (t: any) => t?.execution_state !== 'running' && t?.execution_state !== 'pulling_image';

/** Record what this page writes to its task socket, so a claim can be read off the wire. */
async function watchSocketWrites(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__sent = [] as string[];
    const RealWebSocket = window.WebSocket;
    w.__realSocket = RealWebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        const write = super.send.bind(this);
        this.send = (data: any) => {
          w.__sent.push(String(data));
          write(data);
        };
      }
    };
  });
}

/** The `subscribe_task` frames sent so far, each with the claim it carried. */
async function claimedFrames(page: Page): Promise<Array<{ no_rows?: boolean; task: string }>> {
  return page.evaluate(() => (window as any).__sent as string[])
    .then(frames => frames.map(f => JSON.parse(f))
      .filter(f => f?.type === 'subscribe_task')
      .map(f => ({ no_rows: f.data?.no_rows, task: f.data?.task_id })));
}

/** Keep the newest-window read in flight until the spec releases it. Older-page reads are left alone. */
async function holdWindowRead(page: Page): Promise<() => Promise<void>> {
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let held = 0;
  await page.route(url => url.pathname.endsWith('/messages') && !url.searchParams.has('before'),
    async route => {
      held++;
      await gate;
      await route.continue();
    });
  return async () => {
    expect(held, 'the window read never went out, so this run held nothing').toBeGreaterThan(0);
    release();
  };
}

/** Rows drawn in the transcript, oldest first. */
const entryIds = (page: Page) => page.evaluate(() => Array.from(
  document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'), node => node.dataset.entryId || '',
));

/** The row the reader is looking at, where it sits below the top edge of the scroll region, and how big
 * that region is. The height is the layout question: an element in the flow would take it from the list. */
async function firstVisibleRow(page: Page): Promise<{ id: string; top: number; height: number } | null> {
  return page.evaluate(() => {
    const view = document.querySelector('.chat-messages');
    if (!view) return null;
    const edge = view.getBoundingClientRect().top;
    for (const node of Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))) {
      const box = node.getBoundingClientRect();
      if (box.bottom > edge + 1) {
        return { id: node.dataset.entryId || '', top: Math.round(box.top - edge), height: view.clientHeight };
      }
    }
    return null;
  });
}

/** What this browser holds of the conversation, as its cache holds it, newest role last. */
async function cachedRoles(page: Page, projectId: string, taskId: string): Promise<string[]> {
  return page.evaluate(async ({ pid, tid }) => {
    const db: IDBDatabase = await new Promise((resolve, reject) => {
      const req = indexedDB.open('kanban-storage');
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    try {
      const entry: any = await new Promise((resolve, reject) => {
        const tx = db.transaction('board-cache', 'readonly');
        const req = tx.objectStore('board-cache').get(`messages:${pid}:${tid}`);
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => reject(req.error);
      });
      return (entry?.data?.messages ?? []).map((row: any) => row.role);
    } finally {
      db.close();
    }
  }, { pid: projectId, tid: taskId });
}

function annotate(label: string): void {
  console.log(`MEASURE ${label}`);
  test.info().annotations.push({ type: 'measurement', description: label });
}

test.describe('A Review result read out of the browser while the server is still answering', () => {
  // Short on purpose: with a transcript taller than the scroll region the reader is pinned to its bottom, so
  // which row sits at the top edge is a reading of the layout, not a constant.
  test.use({ viewport: { width: 1280, height: 380 } });

  test('the review card is on screen during the read, and the answered window replaces it once', async ({ page }) => {
    test.setTimeout(180_000);
    const projectId = ownProjectId();
    const taskId = await createTaskViaApi(page, projectId, `provisional-review ${Date.now()} ${REVIEW_PROMPT}`);
    await watchSocketWrites(page);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForTaskState(page, projectId, taskId, t => t.status === 'review' && notInFlight(t), 90_000);

    // The open that fills this browser's cache: the turn is over, so its rows are the newest ones.
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator(REVIEW).first()).toBeVisible({ timeout: 30_000 });
    await expect
      .poll(async () => (await cachedRoles(page, projectId, taskId)).length, { timeout: 15_000 })
      .toBeGreaterThan(0);
    const roles = await cachedRoles(page, projectId, taskId);
    annotate(`cached window: ${roles.length} rows, newest roles ${roles.slice(-3).join(',')}`);

    // Away and back with the read held: nothing but the browser's own copy can paint during the hold.
    const release = await holdWindowRead(page);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 20_000 });
    await page.goto(`/?project=${projectId}&task=${taskId}`);

    await expect(page.locator(PENDING)).toBeVisible({ timeout: 30_000 });
    await expect(page.locator(TRANSCRIPT)).toBeVisible();
    expect(await firstVisibleRow(page), 'the provisional paint left the reader no row at all').not.toBeNull();
    await expect(page.locator(REVIEW).first()).toBeVisible();
    annotate(`held read: ${await entryIds(page).then(ids => ids.length)} rows painted from the cache with the `
      + `review card on screen, notice band=${await page.locator(BAND).count()}`);

    // A wait is not a fault: no band, and no Retry offered beside the rows.
    expect(await page.locator(BAND).count(), 'the pending paint wore the vocabulary of a fault').toBe(0);
    // The paint pins to the bottom, so the jump button has no reason to be up.
    expect(await page.locator('.chat-jump-latest').count(), 'the pending paint and the jump button share a corner')
      .toBe(0);

    // A provisional paint is not the server's rows, so the replay buffer must not be trimmed.
    await expect
      .poll(async () => (await claimedFrames(page)).some(f => f.task === taskId && f.no_rows === true),
        { timeout: 15_000 })
      .toBe(true);

    const before = await firstVisibleRow(page);
    const heldRows = await entryIds(page);
    const style = await page.locator(PENDING).evaluate(node => {
      const cs = getComputedStyle(node);
      return { position: cs.position, painted: node.getBoundingClientRect().height > 0 };
    });
    expect(style.position, 'the floating spinner took a place in the flow').toBe('absolute');
    expect(style.painted, 'the pending indicator never painted, so nothing was measured').toBe(true);

    await release();

    // The answered read replaces both the rows and the fact that anything was pending.
    await expect(page.locator(PENDING)).toHaveCount(0, { timeout: 30_000 });
    await expect(page.locator(REVIEW).first()).toBeVisible();
    const after = await firstVisibleRow(page);
    const ids = await entryIds(page);
    annotate(`answered: ${ids.length} rows (held ${heldRows.length}), first visible row `
      + `${before!.id.slice(0, 8)}@${before!.top}px in a ${before!.height}px list `
      + `→ ${after!.id.slice(0, 8)}@${after!.top}px in a ${after!.height}px list`);

    // Every row once: the cached rows and the answered window overlap at their edge and merge by id.
    expect(new Set(ids).size, 'the answered window drew a row the cache already painted').toBe(ids.length);
    // The spinner is only ever allowed to fade: the row the reader was looking at stays where it was, and so
    // does the height of the region it is drawn in — a floating element takes none of it.
    expect(after).toEqual(before);

    // And the claim is released with the paint: the screen now holds the server's own rows.
    const claims = await claimedFrames(page);
    expect(claims.filter(f => f.task === taskId).at(-1)?.no_rows, 'the claim outlived the read it covered')
      .not.toBe(true);
  });

  test('the pending indicator becomes the fault band when the read it waited on fails', async ({ page }) => {
    test.setTimeout(180_000);
    const projectId = ownProjectId();
    const taskId = await createTaskViaApi(page, projectId, `provisional-fault ${Date.now()} ${REVIEW_PROMPT}`);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForTaskState(page, projectId, taskId, t => t.status === 'review' && notInFlight(t), 90_000);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator(REVIEW).first()).toBeVisible({ timeout: 30_000 });

    // A read that comes back late and broken: the wait has to become the notice, not the empty transcript
    // the cached rows are no longer a claim about.
    await page.route(url => url.pathname.endsWith('/messages') && !url.searchParams.has('before'),
      async route => {
        await new Promise(resolve => setTimeout(resolve, 1200));
        await route.abort('failed');
      });
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 20_000 });
    await page.goto(`/?project=${projectId}&task=${taskId}`);

    await expect(page.locator(PENDING)).toBeVisible({ timeout: 30_000 });
    await expect(page.locator(BAND)).toBeVisible({ timeout: 30_000 });
    const band = (await page.locator(BAND).textContent()) || '';
    const rows = await entryIds(page);
    annotate(`failed read after a provisional paint: band="${band.trim()}" rows=${rows.length} `
      + `spinner=${await page.locator(PENDING).count()}`);
    expect(band.trim(), 'the fault notice arrived without its way out').toContain('Retry');
    expect(await page.locator(PENDING).count(), 'the pending indicator outlived its own answer').toBe(0);
    expect(rows.length, 'the escalation left the reader no transcript at all').toBeGreaterThan(0);
  });

  test('a window that does not end in Review is not painted from the cache', async ({ page }) => {
    test.setTimeout(180_000);
    const projectId = ownProjectId();
    // A text-only turn: the newest row is an assistant answer, so the cached rows stay in the cache.
    const taskId = await createTaskViaApi(page, projectId, `provisional-text ${Date.now()} llm-fixture:text`);
    await ensureTaskStarted(page, projectId, taskId);
    await waitForTaskState(page, projectId, taskId, notInFlight, 90_000);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.chat-entry[data-entry-id]').first()).toBeVisible({ timeout: 30_000 });
    const roles = await cachedRoles(page, projectId, taskId);
    annotate(`cached window ends on "${roles[roles.length - 1]}" — not a request_review result`);
    expect(roles[roles.length - 1]).not.toBe('tool_call');

    const release = await holdWindowRead(page);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 20_000 });
    await page.goto(`/?project=${projectId}&task=${taskId}`);

    // Nothing local is painted, so the screen waits the way it did before: a loading screen, no rows.
    // A cache paint would land on the frame after the held read is handed over, and frames are the
    // window that scales with a loaded machine.
    await waitForReaction(page, 8);
    const rows = await entryIds(page);
    const pending = await page.locator(PENDING).count();
    annotate(`held read on a non-Review window: ${rows.length} rows, pending indicator=${pending}`);
    expect(pending, 'a window ending on an answer was painted from the cache anyway').toBe(0);
    expect(rows, 'a cached window that can still move went on screen').toHaveLength(0);
    await release();
    await expect(page.locator('.chat-entry[data-entry-id]').first()).toBeVisible({ timeout: 30_000 });
  });
});
