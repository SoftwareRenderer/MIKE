import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, getProjectId, createTaskViaApi, navigateToTask, waitForChatForm,
  waitForTaskState, getTaskData, apiGetJson, gotoProject, notInFlight,
  PENDING_TOOL_STATUS as PENDING,
} from './helpers';

isolatedProject();

/**
 * Returning to a task that ran to Review while the tab was away: the frames a frozen page was sent reach
 * JavaScript only once it comes back, so their tokens land after the return signal's read has brought the rows.
 * `long-running-probe` is the turn — one bash call of 20s, then the same answer repeated until the loop
 * detector ends the run in Review.
 * `docs/reconnection.md → The reload meets the replay buffer`
 */

/**
 * While `__away` every frame naming this task is taken off the socket and kept; `__thaw` fires the return
 * signal, then hands the batch back in one task. A listener added in the wrapped constructor runs before the
 * app assigns `onmessage`, which is the only point where a frame can still be removed.
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

const goAway = (page: Page) => page.evaluate(() => { (window as any).__away = true; });

/** The window comes back: the signal first, then the frames that waited behind the freeze. */
const comeBack = (page: Page) => page.evaluate(() => (window as any).__thaw());

/** What the transcript on screen is drawn from, in order. */
async function screenEntryIds(page: Page): Promise<string[]> {
  return page.evaluate(() => Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
    .map(el => el.dataset.entryId || ''));
}

interface Row { id: string; role: string }

/** Every row of the conversation, newest page first paging backwards. */
async function allRows(page: Page, projectId: string, taskId: string): Promise<Row[]> {
  const rows: Row[] = [];
  let cursor = '';
  for (let hop = 0; hop < 10; hop++) {
    const next = await page.evaluate(async ({ pid, tid, before }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages${before ? `?before=${encodeURIComponent(before)}` : ''}`);
      const body = await res.json();
      return {
        body: body as Row[],
        more: res.headers.get('X-Messages-More') === 'true',
        next: res.headers.get('X-Messages-Before') ?? '',
      };
    }, { pid: projectId, tid: taskId, before: cursor });
    rows.push(...next.body);
    if (!next.more || !next.next) return rows;
    cursor = next.next;
  }
  throw new Error('allRows: the conversation did not end within 10 pages');
}

test.describe('returning to a task that ran to Review while the tab was away', () => {
  test('the transcript holds the stored answers, once each', async ({ page }) => {
    test.setTimeout(180000);
    await gotoProject(page);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `long-running-probe ${Date.now()}`);
    await holdFramesWhileAway(page, taskId);
    await navigateToTask(page, taskId, projectId);
    await waitForChatForm(page);
    await expect(page.locator(PENDING)).toBeVisible({ timeout: 90000 });

    await goAway(page);
    await waitForTaskState(page, projectId, taskId, notInFlight, 120000);
    const task = await getTaskData(page, projectId, taskId);
    expect(task.status, 'the probe run was expected to land in Review').toBe('review');
    const fresh = await apiGetJson<any[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`);
    expect(fresh.length, 'the read window came back empty').toBeGreaterThan(0);

    await comeBack(page);
    const released = await page.evaluate(() => (window as any).__released as number);
    expect(released, 'no frame was held behind the freeze, so this run proved nothing').toBeGreaterThan(0);

    // A tool-call row is drawn inside the assistant entry before it, so it is not an entry of its own.
    const stored = (await allRows(page, projectId, taskId))
      .filter(m => m.role !== 'title' && m.role !== 'tool_call')
      .map(m => m.id);
    const expectedWindow = fresh.filter(m => m.role !== 'title' && m.role !== 'tool_call').map(m => m.id);
    await expect(async () => {
      const onScreen = await screenEntryIds(page);
      expect(onScreen.filter(id => !stored.includes(id))).toEqual([]);
      expect(onScreen.slice(-expectedWindow.length)).toEqual(expectedWindow);
    }).toPass({ timeout: 20000 });

    expect(await page.locator(PENDING).count(), 'a card left running for a task that stopped').toBe(0);
    expect(await page.locator('.cursor-blink').count(), 'a turn left streaming that the server finished').toBe(0);
  });
});
