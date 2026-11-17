import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, createTaskViaApi, navigateToTask, waitForChatForm,
  waitForTaskState, getProjectId, gotoProject, getTaskData, apiGetJson,
  runHasEnded, notInFlight, stopReading, comeBack, sendTurn, watchTheReturn,
  PENDING_TOOL_STATUS as PENDING,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

// A mounted chat that was unread comes back to the stored rows — the same claim the tab-return reconciliation
// makes, asked of two different arrivals:
//
//   1. the frames were only held back, and reach JavaScript after the return signal's read has brought the
//      rows, so their tokens must land beside the rows rather than over them; and
//   2. six turns ran with no socket at all, so the window the chat holds when it comes back no longer
//      covers what it drew before the window closed.
//
// Both are decided by identity: an entry is the row it was read from, and the transcript is that row's
// order. `docs/reconnection.md → The tab that comes back`, `docs/api-contract.md → The conversation is a
// window, not the thread`.

/** The widest page the messages route allows. */
const ROW_LIMIT = 25;
/** A `tool_call` row draws inside the entry above it, so it names no entry of its own. */
const isEntryRow = (role: string) => role !== 'title' && role !== 'tool_call';

interface Row { id: string; role: string }

/**
 * The harder variant: while `__away` every frame naming this task is taken off the socket and KEPT, and
 * `__thaw` fires the return signal and then hands the batch back in one task — the ordering a woken page
 * actually produces, rows first and the turn's tokens after them.
 */
async function holdFramesWhileAway(page: Page, taskId: string): Promise<void> {
  await page.addInitScript((id) => {
    const w = window as any;
    w.__away = false;
    w.__held = [] as string[];
    w.__released = 0;
    w.__sockets = [] as WebSocket[];
    w.__thaw = () => {
      w.__away = false;
      document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
      const batch: string[] = w.__held.splice(0, w.__held.length);
      w.__released += batch.length;
      // The newest socket only: the app reads one stream, and a frame delivered twice is content twice.
      const sock: WebSocket | undefined = w.__sockets[w.__sockets.length - 1];
      if (sock) for (const data of batch) sock.dispatchEvent(new MessageEvent('message', { data }));
    };
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        w.__sockets.push(this);
        this.addEventListener('message', (e: MessageEvent) => {
          if (!w.__away) return;
          let frame: unknown;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (!JSON.stringify(frame).includes(id)) return;
          e.stopImmediatePropagation();
          w.__held.push(String(e.data));
        });
      }
    };
  }, taskId);
}

const swallowed = (page: Page) => page.evaluate(() => (window as any).__swallowed as number);
const released = (page: Page) => page.evaluate(() => (window as any).__released as number);

/** The entry ids on screen, top to bottom. */
async function onScreen(page: Page): Promise<string[]> {
  return page.evaluate(() => Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
    .map(node => node.dataset.entryId || ''));
}

async function screenText(page: Page): Promise<string[]> {
  return page.evaluate(() => Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
    .map(node => `${(node.dataset.entryId || '').slice(0, 8)} ${node.textContent?.replace(/\s+/g, ' ').trim().slice(0, 30)}`));
}

/** Every stored row of the thread, oldest first, walking the window route's own cursor to the start. */
async function allRows(page: Page, projectId: string, taskId: string, limit = ROW_LIMIT): Promise<Row[]> {
  const pages = await page.evaluate(async ({ pid, tid, rowLimit }) => {
    const collected: Array<Row[]> = [];
    let cursor = '';
    for (let hop = 0; hop < 10; hop++) {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages?limit=${rowLimit}${
        cursor ? `&before=${encodeURIComponent(cursor)}` : ''}`);
      collected.push(await res.json() as Row[]);
      const more = res.headers.get('X-Messages-More') === 'true';
      cursor = res.headers.get('X-Messages-Before') ?? '';
      if (!more || !cursor) break;
    }
    return collected;
  }, { pid: projectId, tid: taskId, rowLimit: limit });
  // Each page answers oldest first; the pages arrive newest window first.
  return pages.reverse().flat();
}

/** The ids of every stored row the transcript draws an entry for. */
async function storedIds(page: Page, projectId: string, taskId: string): Promise<string[]> {
  return (await allRows(page, projectId, taskId)).filter(r => isEntryRow(r.role)).map(r => r.id);
}

/** Does the conversation overflow one window as the app asks for it — no `limit`, so the server's own
 *  default (`store.MessageWindowDefault`)? False means the ordering this spec measures would hold by
 *  accident, because nothing sat outside the newest page. */
async function newestWindowIsPartial(page: Page, projectId: string, taskId: string): Promise<boolean> {
  return page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
    return res.headers.get('X-Messages-More') === 'true';
  }, { pid: projectId, tid: taskId });
}

/** The conversation has stopped moving: nothing streams, and the stored rows no longer grow. */
async function conversationSettles(page: Page, projectId: string, taskId: string): Promise<void> {
  let previous = '';
  await expect.poll(async () => {
    if (await page.locator('.cursor-blink, .tool-status.pending').count() > 0) { previous = ''; return false; }
    const ids = (await storedIds(page, projectId, taskId)).join(',');
    const still = ids !== '' && ids === previous;
    previous = ids;
    return still;
  }, { timeout: 90000, intervals: [1500] }).toBe(true);
}

async function assertRowOrder(page: Page, projectId: string, taskId: string): Promise<void> {
  const screen = await onScreen(page);
  const stored = await storedIds(page, projectId, taskId);
  const from = stored.lastIndexOf(screen[0]);
  const inOrder = from >= 0 && screen.join() === stored.slice(from).join();
  const problems = [
    ...screen.filter(id => !stored.includes(id)).map(id => `entry ${id} has no row behind it`),
    ...(new Set(screen).size === screen.length ? [] : ['an entry is drawn twice']),
    ...(screen.at(-1) === stored.at(-1) ? [] : [`the transcript ends on ${screen.at(-1)}, below which sits no newer row`]),
    ...(inOrder ? [] : ['the transcript is not the stored conversation in order']),
  ];
  if (problems.length) {
    console.log('ORDER problems:', JSON.stringify(problems));
    console.log('ORDER screen:', JSON.stringify(await screenText(page)));
    console.log('ORDER stored:', JSON.stringify(stored));
  }
  expect(problems, 'the transcript is not the stored conversation, in order').toEqual([]);
}

test.describe('A mounted chat that missed a turn', () => {
  test('the frames held behind the freeze land beside the stored answers, once each', async ({ page }) => {
    test.setTimeout(180_000);
    await gotoProject(page);
    const projectId = await getProjectId(page);
    // `long-running-probe`: one bash call of 20s, then the same answer repeated until the loop
    // detector ends the run in Review.
    const taskId = await createTaskViaApi(page, projectId, `long-running-probe ${Date.now()}`);
    await holdFramesWhileAway(page, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(PENDING)).toBeVisible({ timeout: 90000 });

    await page.evaluate(() => { (window as any).__away = true; });
    await waitForTaskState(page, projectId, taskId, notInFlight, 120_000);
    const task = await getTaskData(page, projectId, taskId);
    expect(task.status, 'the probe run was expected to land in Review').toBe('review');
    const fresh = await apiGetJson<Row[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`);
    expect(fresh.length, 'the read window came back empty').toBeGreaterThan(0);

    await page.evaluate(() => (window as any).__thaw());
    expect(await released(page), 'no frame was held behind the freeze, so this run proved nothing')
      .toBeGreaterThan(0);

    const stored = (await allRows(page, projectId, taskId)).filter(r => isEntryRow(r.role)).map(r => r.id);
    const expectedWindow = fresh.filter(r => isEntryRow(r.role)).map(r => r.id);
    await expect(async () => {
      const drawn = await onScreen(page);
      expect(drawn.filter(id => !stored.includes(id)), 'an entry no row backs').toEqual([]);
      expect(drawn.slice(-expectedWindow.length), 'the transcript does not end on the read window')
        .toEqual(expectedWindow);
    }).toPass({ timeout: 20000 });

    expect(await page.locator(PENDING).count(), 'a card left running for a task that stopped').toBe(0);
    expect(await page.locator('.cursor-blink').count(), 'a turn left streaming that the server finished').toBe(0);
  });

  test('comes back to the stored rows in order, with nothing older below them', async ({ page }) => {
    test.setTimeout(180_000);
    await gotoProject(page);
    await watchTheReturn(page);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `slow-tool-probe long-away ${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(PENDING)).toBeVisible({ timeout: 90000 });
    await waitForTaskState(page, projectId, taskId, runHasEnded, 120000);

    // Six more turns run unread. One prompt is one unit and the messages route answers five units by
    // default, so this is the case the ordering has to survive: the window the chat holds when it comes
    // back no longer covers what it drew before the window closed.
    await stopReading(page);
    for (let turn = 1; turn <= 6; turn++) {
      await sendTurn(page, projectId, taskId, `unread turn ${turn} llm-fixture:text`);
      await waitForTaskState(page, projectId, taskId, runHasEnded, 120000);
    }
    expect(await swallowed(page), 'no frame was dropped, so this run proved nothing').toBeGreaterThan(0);

    await comeBack(page);
    // The premise, asserted: without this the ordering would hold of itself.
    expect(await newestWindowIsPartial(page, projectId, taskId),
      'the whole conversation fits in one window, so ordering had nothing to cross').toBe(true);
    await conversationSettles(page, projectId, taskId);
    await assertRowOrder(page, projectId, taskId);

    // Reading up from there adds rows to the top: never a second copy, never an answer out of order.
    await page.evaluate(() => {
      const el = document.querySelector('.chat-messages');
      if (el) el.scrollTop = 0;
    });
    await expect(async () => {
      await assertRowOrder(page, projectId, taskId);
    }).toPass({ timeout: 20000 });
  });
});
