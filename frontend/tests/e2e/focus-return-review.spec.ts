import { test, expect, type Page } from '@playwright/test';
import {
  isolatedProject, createTaskViaApi, navigateToTask, waitForChatForm,
  waitForTaskState, getProjectId, gotoProject,
} from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();

/**
 * The chat is on screen when the window stops being read, and the turn finishes while its frames are dropped.
 * The return reads the message window unconditionally, because the digest says the conversation moved and the
 * held baseline cannot answer for it; the following return is the one the ETag answers.
 * `docs/reconnection.md → The tab that comes back`
 */

const PENDING = '.tool-status.pending';
const CARD = '.tool-call-card';
const TRANSCRIPT = '.chat-content';
const OUTPUT = 'SLOW-TOOL-PROBE-DONE';
const ANSWER = 'The command completed successfully';
/** The end of the run: the state after which nothing more is written to the conversation. */
const runIsOver = (t: any) => t?.status === 'review' && t?.execution_state === 'failed';

/**
 * While `window.__unfocused` the page reports no focus; while `window.__away` every frame but the keepalive
 * is dropped before the app sees it, because a listener added in the wrapped constructor runs before the app
 * assigns `onmessage`. The `If-None-Match` recorded here is the one the app itself passed — a browser
 * revalidating its own HTTP cache sends that header too.
 */
async function watchTheReturn(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__swallowed = 0;
    w.__signals = { visibilitychange: 0, focus: 0, pageshow: 0 };
    w.__revisions = [] as Array<string | null>;
    w.__sync = 0;
    w.__mutations = 0;
    w.__observer = null as MutationObserver | null;
    for (const type of Object.keys(w.__signals)) {
      window.addEventListener(type, () => { w.__signals[type]++; });
    }
    Object.defineProperty(document, 'hasFocus', {
      configurable: true,
      get: () => () => !w.__unfocused,
    });
    const realFetch = window.fetch;
    window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input
        : input instanceof URL ? input.href : (input as Request).url;
      if (url.includes('/sync')) w.__sync++;
      if (url.includes('/messages')) {
        w.__revisions.push(new Headers(init?.headers).get('If-None-Match'));
      }
      return realFetch(input as any, init);
    }) as typeof window.fetch;
    /** Count DOM changes inside the transcript. */
    w.__watchTranscript = () => {
      const target = document.querySelector('.chat-content');
      if (!target) return false;
      w.__observer?.disconnect();
      w.__observer = new MutationObserver((records) => { w.__mutations += records.length; });
      w.__observer.observe(target, { childList: true, subtree: true, characterData: true });
      w.__mutations = 0;
      return true;
    };
    const RealWebSocket = window.WebSocket;
    window.WebSocket = class extends RealWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols);
        if (!String(url).includes('/api/stream')) return;
        this.addEventListener('message', (e: MessageEvent) => {
          if (!w.__away) return;
          let frame: any;
          try { frame = JSON.parse(String(e.data)); } catch { return; }
          if (frame?.type === 'ping') return; // the keepalive is not content: the socket stays live
          w.__swallowed++;
          e.stopImmediatePropagation();
        });
      }
    };
  });
}

/** The window stops being read: it reports no focus, and no frame reaches JavaScript. */
const stopReading = (page: Page) =>
  page.evaluate(() => { (window as any).__unfocused = true; (window as any).__away = true; });

/** The window comes back. Only `focus` is dispatched — the page was never hidden. */
const comeBack = (page: Page) => page.evaluate(() => {
  const w = window as any;
  w.__away = false;
  w.__unfocused = false;
  window.dispatchEvent(new Event('focus'));
});

const seen = (page: Page) => page.evaluate(() => ({
  swallowed: (window as any).__swallowed as number,
  signals: (window as any).__signals as Record<string, number>,
  revisions: (window as any).__revisions as Array<string | null>,
  sync: (window as any).__sync as number,
  mutations: (window as any).__mutations as number,
}));

/** Whether a read after the ones already counted went out without a baseline. */
const unconditionalAfter = (revisions: Array<string | null>, from: number): boolean =>
  revisions.slice(from).some(v => v === null);

/** The conditional reads that went out after the ones already counted. */
const conditionalAfter = (revisions: Array<string | null>, from: number): number =>
  revisions.slice(from).filter(v => typeof v === 'string').length;

/** What the transcript is drawn from, in order, plus the text the reader sees. */
async function transcript(page: Page): Promise<{ ids: string[]; text: string }> {
  return page.evaluate(() => ({
    ids: Array.from(document.querySelectorAll<HTMLElement>('.chat-entry[data-entry-id]'))
      .map(node => node.dataset.entryId || ''),
    text: document.querySelector('.chat-content')?.textContent || '',
  }));
}

/** One page of stored rows, newest page first, with the cursor that continues backwards. */
async function rowPage(page: Page, projectId: string, taskId: string, before: string) {
  return page.evaluate(async ({ pid, tid, cursor }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages?limit=25${
      cursor ? `&before=${encodeURIComponent(cursor)}` : ''}`);
    return {
      rows: await res.json() as Array<{ id: string; role: string }>,
      more: res.headers.get('X-Messages-More') === 'true',
      next: res.headers.get('X-Messages-Before') ?? '',
    };
  }, { pid: projectId, tid: taskId, cursor: before });
}

/** Every row of the conversation, newest page first. A tool card is drawn inside the assistant entry that
 * precedes it, so a `tool_call` row is not an entry of its own. */
async function storedIds(page: Page, projectId: string, taskId: string): Promise<string[]> {
  const rows: Array<{ id: string; role: string }> = [];
  let cursor = '';
  for (let hop = 0; hop < 10; hop++) {
    const next = await rowPage(page, projectId, taskId, cursor);
    rows.push(...next.rows);
    if (!next.more || !next.next) {
      return rows.filter(r => r.role !== 'title' && r.role !== 'tool_call').map(r => r.id);
    }
    cursor = next.next;
  }
  throw new Error('storedIds: the conversation did not end within 10 pages');
}

function annotate(label: string): void {
  console.log(`MEASURE ${label}`);
  test.info().annotations.push({ type: 'measurement', description: label });
}

test.describe('A mounted chat returned to by the window focus event', () => {
  test('the answer and the stored tool result come back, and a second return paints nothing',
    async ({ page }) => {
      test.setTimeout(180000);
      await gotoProject(page);
      await watchTheReturn(page);
      const projectId = await getProjectId(page);
      const taskId = await createTaskViaApi(page, projectId, `slow-tool-probe focus-return ${Date.now()}`);
      await navigateToTask(page, taskId, projectId);
      await waitForChatForm(page);
      // The call is announced and still unanswered. Turns queue behind every other worker's turn, so this
      // waits on the executor, not on the DOM.
      await expect(page.locator(PENDING)).toBeVisible({ timeout: 90000 });
      const before = await seen(page);

      // The window stops being read for the rest of the run.
      await stopReading(page);
      await waitForTaskState(page, projectId, taskId, runIsOver, 120000);
      annotate(`unfocused: frames dropped=${(await seen(page)).swallowed} signals=${JSON.stringify(
        (await seen(page)).signals)}`);
      expect((await seen(page)).swallowed, 'no frame was dropped, so this run proved nothing')
        .toBeGreaterThan(0);
      // The transcript is still behind and the card still reads as running.
      await expect(page.locator(PENDING)).toBeVisible();
      expect((await transcript(page)).text, 'the answer arrived while the window was unread').not
        .toContain(ANSWER);

      // Start counting changes here, so the second return's silence is measured against a return that painted.
      expect(await page.evaluate(() => (window as any).__watchTranscript())).toBe(true);
      await comeBack(page);

      await expect(page.locator(PENDING), 'a card left running for a task in Review').toHaveCount(0,
        { timeout: 20000 });
      await expect(page.locator(CARD).first(), 'the stored tool result never came back').toContainText(OUTPUT,
        { timeout: 20000 });
      await expect(page.locator(TRANSCRIPT), 'the turn\'s final text never came back').toContainText(ANSWER,
        { timeout: 20000 });
      await expect(page.locator('.cursor-blink')).toHaveCount(0);

      const returned = await seen(page);
      annotate(`first return: mutations in the transcript=${returned.mutations}`);
      expect(returned.mutations, 'the changed revision left the transcript as it was').toBeGreaterThan(0);
      expect(returned.signals.visibilitychange, 'a visibilitychange would be a different return').toBe(
        before.signals.visibilitychange);
      expect(returned.signals.pageshow).toBe(before.signals.pageshow);
      expect(returned.signals.focus, 'the gesture never reached the window').toBeGreaterThan(
        before.signals.focus);
      // The return asked the digest, and the read that followed it named no baseline.
      annotate(`first return: sync calls ${before.sync} -> ${returned.sync}, ` +
        `reads=${returned.revisions.length - before.revisions.length}`);
      expect(returned.sync, 'the return asked nothing of the server').toBeGreaterThan(before.sync);
      expect(unconditionalAfter(returned.revisions, before.revisions.length),
        'the return revalidated a baseline instead of reading the rows').toBe(true);

      // Nothing on screen lacks a stored row, nothing is drawn twice, and the newest rows sit at the bottom.
      const newest = await rowPage(page, projectId, taskId, '');
      const expected = newest.rows.filter(r => r.role !== 'title' && r.role !== 'tool_call').map(r => r.id);
      const stored = await storedIds(page, projectId, taskId);
      const onScreen = (await transcript(page)).ids;
      expect(onScreen.filter(id => !stored.includes(id)), 'content with no row behind it').toEqual([]);
      expect(new Set(onScreen).size, 'an entry drawn twice').toBe(onScreen.length);
      expect(onScreen.slice(-expected.length)).toEqual(expected);

      // Coming back to a conversation that has not moved leaves the transcript alone: a 304 has no body.
      await stopReading(page);
      await page.waitForTimeout(2500); // longer than one return gesture's dedupe
      const settled = await transcript(page);
      expect(await page.evaluate(() => (window as any).__watchTranscript())).toBe(true);
      const idle = await seen(page);
      await comeBack(page);
      await expect(async () => {
        const after = await seen(page);
        expect(conditionalAfter(after.revisions, idle.revisions.length),
          'the second return asked the server nothing').toBeGreaterThan(0);
      }).toPass({ timeout: 20000 });
      // Wait briefly for a paint: a replaced entry list changes this DOM.
      await page.waitForTimeout(1000);
      const quiet = await seen(page);
      annotate(`second return: mutations in the transcript=${quiet.mutations}`);
      expect(quiet.mutations, 'an unchanged revision still re-painted the transcript').toBe(0);
      const unchanged = await transcript(page);
      expect(unchanged.ids).toEqual(settled.ids);
      expect(unchanged.text).toEqual(settled.text);
    });
});
