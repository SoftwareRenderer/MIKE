import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, createTaskViaApi, navigateToTask, waitForChatForm,
  waitForTaskState, getProjectId, gotoProject, runHasEnded, stopReading, comeBack, sendTurn,
  PENDING_TOOL_STATUS as PENDING,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/** The widest page the messages route allows. */
const ROW_LIMIT = 25;
/** A `tool_call` row draws inside the entry above it, so it names no entry of its own. */
const isEntryRow = (role: string) => role !== 'title' && role !== 'tool_call';

/**
 * While `window.__away` no frame reaches the app and `document.hasFocus()` answers false. The listener goes in
 * the wrapped constructor because the app assigns `onmessage` later.
 */
async function watchTheReturn(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__swallowed = 0;
    Object.defineProperty(document, 'hasFocus', {
      configurable: true,
      get: () => () => !w.__unfocused,
    });
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          if (!w.__away) return;
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (frame?.type === 'ping') return; // a ping is not content: the socket stays live
          w.__swallowed++;
          e.stopImmediatePropagation();
        });
      }
    };
  });
}

const swallowed = (page: Page) => page.evaluate(() => (window as any).__swallowed as number);

/** The entry ids on screen, top to bottom. */
async function onScreen(page: Page): Promise<string[]> {
  return page.evaluate(() => Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
    .map(node => node.dataset.entryId || ''));
}

async function screenText(page: Page): Promise<string[]> {
  return page.evaluate(() => Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
    .map(node => `${(node.dataset.entryId || '').slice(0, 8)} ${node.textContent?.replace(/\s+/g, ' ').trim().slice(0, 30)}`));
}

/** Every stored row of the thread, oldest first. */
async function storedIds(page: Page, projectId: string, taskId: string): Promise<string[]> {
  const pages = await page.evaluate(async ({ pid, tid, limit }) => {
    const collected: Array<Array<{ id: string; role: string }>> = [];
    let cursor = '';
    for (let hop = 0; hop < 10; hop++) {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages?limit=${limit}${
        cursor ? `&before=${encodeURIComponent(cursor)}` : ''}`);
      collected.push(await res.json() as Array<{ id: string; role: string }>);
      const more = res.headers.get('X-Messages-More') === 'true';
      cursor = res.headers.get('X-Messages-Before') ?? '';
      if (!more || !cursor) break;
    }
    return collected;
  }, { pid: projectId, tid: taskId, limit: ROW_LIMIT });
  // Each page answers oldest first; the pages arrive newest window first.
  return pages.reverse().flat().filter(r => isEntryRow(r.role)).map(r => r.id);
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

// A conversation longer than one window comes back as the stored rows in the order they were written: a window
// may hold no more than the newest tail, and never an answer out of its place.
// `docs/reconnection.md → The tab that comes back`
test.describe('A mounted chat that missed a conversation longer than one window', () => {
  test('comes back to the stored rows in order, with nothing older below them', async ({ page }) => {
    test.setTimeout(180000);
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
