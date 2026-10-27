import { test, expect, type Page } from '@playwright/test';
import { isolatedProject, gotoProject, getProjectId, csrfFetch } from './helpers';

/**
 * Measurement, not a gate: where a task open spends the time between the navigation and the first painted
 * transcript row, and which read owns it.
 *
 * The open path in `TaskChat.svelte`'s `onMount` asks the thread list, the task row and the newest window at
 * once — the window route answers for whichever thread is current and names none, so none of the three waits
 * on another. Whether the round trips or one slow read are the bottleneck decides between asking them
 * differently and answering the first paint from IndexedDB, so both numbers are reported here.
 *
 * The assertions are structural — the open's shape, the boot's shape, and what a read that does not answer
 * leaves the reader holding. A threshold on a millisecond figure would make this the flakiest spec in the
 * tree.
 */
isolatedProject();

// Enough rows that older ones exist behind the newest window — the state a returning reader is in.
const SEED_ROWS = 60;

function annotate(label: string): void {
  console.log(`MEASURE ${label}`);
  test.info().annotations.push({ type: 'measurement', description: label });
}

/** Seed a conversation one row at a time, oldest first: rows without an executor turn, at any size. */
async function seedConversation(
  page: Page, projectId: string, taskId: string, rows: number, text: string = defaultBody,
): Promise<void> {
  const csrf = await page.evaluate(async () =>
    (await (await fetch('/api/auth/session')).json()).csrf_token || '');
  const seeded = await page.evaluate(async ({ pid, tid, count, token, body }) => {
    let made = 0;
    for (let i = 0; i < count; i++) {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) },
        credentials: 'include',
        body: JSON.stringify({ role: 'user', content: body.replace('#i#', String(i)) }),
      });
      if (!res.ok) return { made, status: res.status, text: await res.text() };
      made++;
    }
    return { made, status: 200, text: '' };
  }, { pid: projectId, tid: taskId, count: rows, token: csrf, body: text });
  expect(seeded.status, `seeding stopped at row ${seeded.made}: ${seeded.text}`).toBe(200);
}

/** ~1.4 kB of prose per row: a newest window with a parse and a render cost, not a 300-byte stub. */
const defaultBody = `seed row #i#\n${'filler text that gives the entry some height '.repeat(30)}`;

/** ~20 kB of high-entropy text per row. Compressible filler would answer a bandwidth question with the
 * gzip ratio instead of the payload, and a pasted log is not compressible prose. */
function fatBody(): string {
  const chunk = () => Math.random().toString(36).slice(2);
  let text = 'pasted log #i#\n';
  while (text.length < 20_000) text += chunk() + ' ';
  return text;
}

/** Record the open on the page's own clock, before any app script runs.
 * `performance.now()` starts at navigation start, so every figure below is "ms after the click". */
async function installProbe(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    const WATCH_AFTER_ROW = 1500;
    w.__probe = { firstChatAt: 0, firstRowAt: 0, mirrorAt: 0, done: false, mutations: 0, samples: [] };
    // A busy task page asks more endpoints than the default buffer holds.
    if (performance.setResourceTimingBufferSize) performance.setResourceTimingBufferSize(2000);
    const rowCount = () => document.querySelectorAll('.chat-messages .chat-entry[data-entry-id]').length;
    const listHeight = () => (document.querySelector('.chat-messages') as HTMLElement | null)?.scrollHeight ?? 0;
    new MutationObserver((m) => {
      const p = w.__probe;
      p.mutations += m.length;
      if (!p.firstChatAt && document.querySelector('.chat-messages')) p.firstChatAt = performance.now();
      // The offline mirror screen: the boot's own answer to a server that is slow rather than gone.
      if (!p.mirrorAt && document.querySelector('.offline-btn')) p.mirrorAt = performance.now();
      if (!p.firstRowAt && rowCount()) p.firstRowAt = performance.now();
    }).observe(document, { childList: true, subtree: true });
    // Entries are realized as they approach the viewport, so the DOM count keeps moving after the first
    // row. Sample it for a fixed window and let the reader of the measurement say when it stopped.
    const id = setInterval(() => {
      const p = w.__probe;
      const now = performance.now();
      if (!p.firstRowAt) { p.firstRowAt = rowCount() ? now : 0; if (!p.firstRowAt) return; }
      const n = rowCount();
      p.samples.push([Math.round(now), n, listHeight()]);
      if (now - p.firstRowAt > WATCH_AFTER_ROW) { p.done = true; clearInterval(id); }
    }, 16);
  });
}

interface ApiCall {
  path: string;
  start: number; requestStart: number; responseStart: number; end: number;
  server: number; bytes: number; wire: number;
}

interface Harvest {
  firstChatAt: number; firstRowAt: number; filledAt: number; lastChangeAt: number;
  rows: number; height: number; mutations: number; docLoad: number; mirrorAt: number;
  apiBytes: number; assetBytes: number;
  /** Reads the browser revalidated rather than downloaded: a 304 means the body never crossed the wire. */
  revalidated: string[];
  calls: ApiCall[]; samples: Array<[number, number, number]>;
}

/** Everything the page asked for, on the same clock as the paint marks. */
async function harvest(page: Page): Promise<Harvest> {
  return page.evaluate(() => {
    const p = (window as any).__probe;
    const nav = performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming | undefined;
    const resources = performance.getEntriesByType('resource') as PerformanceResourceTiming[];
    // `transferSize` is what crossed the wire: 0 for a response the service worker's precache served.
    // `encodedBodySize` is the compressed body, which is what a capped link would carry if it had to fetch.
    const bytesOf = (api: boolean, field: 'transferSize' | 'encodedBodySize') => resources
      .filter(r => (new URL(r.name).pathname.startsWith('/api/')) === api)
      .reduce((sum, r) => sum + r[field], 0);
    const calls: ApiCall[] = resources
      .filter(r => new URL(r.name).pathname.startsWith('/api/'))
      .map(r => {
        const u = new URL(r.name);
        const path = u.pathname
          .replace(/\/api\/projects\/(?!counts)[^/]+/, '/api/…')
          .replace(/\/tasks\/[^/]+/, '/task')
          .replace(/\/messages\/[^/]+/, '/messages/…') + u.search;
        return {
          path,
          start: Math.round(r.startTime),
          requestStart: Math.round(r.requestStart),
          responseStart: Math.round(r.responseStart),
          end: Math.round(r.responseEnd),
          server: Math.round(r.responseStart - r.requestStart),
          bytes: r.encodedBodySize,
          wire: r.transferSize,
        };
      })
      .sort((a, b) => a.start - b.start);
    const samples: Array<[number, number, number]> = p.samples;
    const view = document.querySelector('.chat-messages') as HTMLElement | null;
    // When the transcript stopped growing: the last sample at which its rows or its height moved. The
    // open reads a 5-row window and fills the rest of the viewport a page at a time, so this is the end
    // of the reader's wait rather than the first painted row.
    let lastChangeAt = samples.length ? samples[0][0] : 0;
    for (let i = 1; i < samples.length; i++) {
      if (samples[i][1] !== samples[i - 1][1] || samples[i][2] !== samples[i - 1][2]) lastChangeAt = samples[i][0];
    }
    const filledAt = samples.find(s => view && s[2] >= view.clientHeight)?.[0] ?? 0;
    return {
      firstChatAt: Math.round(p.firstChatAt), firstRowAt: Math.round(p.firstRowAt), filledAt,
      lastChangeAt, rows: samples.length ? samples[samples.length - 1][1] : 0,
      height: samples.length ? samples[samples.length - 1][2] : 0,
      mutations: p.mutations, mirrorAt: Math.round(p.mirrorAt),
      apiBytes: bytesOf(true, 'transferSize'), assetBytes: bytesOf(false, 'transferSize'),
      revalidated: [],
      docLoad: nav ? Math.round(nav.loadEventEnd) : 0, calls, samples,
    };
  });
}

function report(label: string, h: Harvest): void {
  const win = h.calls.find(c => c.path === '/api/…/task/messages');
  const pages = h.calls.filter(c => c.path.startsWith('/api/…/task/messages?before')).length;
  const chain = h.calls.filter(c => [
    '/api/…/task/threads', '/api/…/task', '/api/…/task/messages',
  ].includes(c.path));
  const serial = chain.length === 3 ? chain[2].start - chain[0].requestStart : -1;
  annotate(`${label}: document ready +${h.docLoad}ms → chat shell +${h.firstChatAt}ms → first row `
    + `+${h.firstRowAt}ms → viewport covered +${h.filledAt}ms → transcript stopped growing +${h.lastChangeAt}ms `
    + `(${h.rows} rows in the DOM of a ${h.height}px list, ${pages} older-page reads, ${h.mutations} mutations, `
    + `${h.apiBytes} B of API and ${h.assetBytes} B of assets crossed the wire, `
    + `${h.revalidated.length} read(s) answered 304 [${h.revalidated.join(', ')}])`);
  annotate(`${label}: threads + row + messages occupy ${serial}ms of wall clock `
    + `[${chain.map(c => `${c.path.split('/task')[1] || c.path}@${c.start}..${c.end}`).join(' + ')}]; `
    + `window read ends +${win ? win.end : -1}ms, so render is `
    + `${win ? h.firstRowAt - win.end : -1}ms of the ${h.firstRowAt}ms`);
  console.log(`${label}: every read the open made\n`
    + h.calls.map(c => `  +${String(c.start).padStart(5)}ms → +${String(c.end).padStart(5)}ms `
      + `server=${String(c.server).padStart(4)}ms wire=${String(c.wire).padStart(6)} `
      + `body=${String(c.bytes).padStart(6)}  ${c.path}`).join('\n'));
  console.log(`${label}: transcript rows and height over time (ms:rows:px) `
    + h.samples.filter((s, i, a) => i === 0 || s[1] !== a[i - 1][1] || s[2] !== a[i - 1][2])
      .map(s => s.join(':')).join(' '));
}

/** What each read costs on its own, warm, with nothing else competing — the ceiling on any saving from
 * answering one of them differently. */
async function endpointCosts(page: Page, projectId: string, taskId: string): Promise<Record<string, ApiCall>> {
  return page.evaluate(async ({ pid, tid }) => {
    const paths: Record<string, string> = {
      threads: `/api/projects/${pid}/tasks/${tid}/threads`,
      row: `/api/projects/${pid}/tasks/${tid}`,
      sync: `/api/projects/${pid}/tasks/${tid}/sync`,
      messages: `/api/projects/${pid}/tasks/${tid}/messages`,
      turns: `/api/projects/${pid}/tasks/${tid}/turns`,
    };
    const out: Record<string, any> = {};
    for (const [name, url] of Object.entries(paths)) {
      const samples: number[] = [];
      let bytes = 0;
      for (let i = 0; i < 7; i++) {
        const t0 = performance.now();
        const res = await fetch(url, { cache: 'no-store' });
        const body = await res.text();
        samples.push(performance.now() - t0);
        bytes = body.length;
      }
      samples.sort((a, b) => a - b);
      out[name] = { path: name, start: 0, requestStart: 0, responseStart: 0, end: 0,
        server: Math.round(samples[3]), bytes, wire: bytes };
    }
    return out;
  }, { pid: projectId, tid: taskId });
}

/** The chain the open makes, asked in series (as `onMount` does) and in parallel (what unchaining it
 * would do). The gap between the two is what an unchained open could hand back, in this condition. */
async function chainCost(page: Page, projectId: string, taskId: string) {
  return page.evaluate(async ({ pid, tid }) => {
    const base = `/api/projects/${pid}/tasks/${tid}`;
    const urls = [`${base}/threads`, base, `${base}/messages`];
    const grab = async (url: string) => (await (await fetch(url, { cache: 'no-store' })).text()).length;
    const runs = async (fn: () => Promise<unknown>): Promise<number> => {
      const best: number[] = [];
      for (let i = 0; i < 5; i++) {
        const t0 = performance.now();
        await fn();
        best.push(performance.now() - t0);
      }
      best.sort((a, b) => a - b);
      return Math.round(best[2]);
    };
    const serial = await runs(async () => { for (const u of urls) await grab(u); });
    const parallel = await runs(async () => { await Promise.all(urls.map(grab)); });
    const one = await runs(async () => { await grab(urls[2]); });
    return { serial, parallel, one };
  }, { pid: projectId, tid: taskId });
}

/** Add latency and cap the throughput, the way a phone on a bad cell link would cost. */
async function throttle(page: Page, opts: { rtt: number; downKbps?: number; upKbps?: number }): Promise<void> {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.emulateNetworkConditions', {
    offline: false,
    latency: opts.rtt,
    downloadThroughput: opts.downKbps ? Math.round(opts.downKbps * 1024 / 8) : -1,
    uploadThroughput: opts.upKbps ? Math.round(opts.upKbps * 1024 / 8) : -1,
  });
}

/** What reading this task's cached window out of IndexedDB costs, and what is in it. */
async function cacheReadCost(page: Page, projectId: string, taskId: string) {
  return page.evaluate(async ({ pid, tid }) => {
    const key = `messages:${pid}:${tid}`;
    const db: IDBDatabase = await new Promise((res, rej) => {
      const req = indexedDB.open('kanban-storage');
      req.onsuccess = () => res(req.result);
      req.onerror = () => rej(req.error);
    });
    let rows = 0;
    let bytes = 0;
    const samples: number[] = [];
    for (let i = 0; i < 7; i++) {
      const t0 = performance.now();
      const entry: any = await new Promise((res, rej) => {
        const tx = db.transaction('board-cache', 'readonly');
        const req = tx.objectStore('board-cache').get(key);
        req.onsuccess = () => res(req.result);
        req.onerror = () => rej(req.error);
      });
      samples.push(performance.now() - t0);
      if (i === 0) {
        rows = entry?.data?.messages?.length ?? 0;
        bytes = entry ? JSON.stringify(entry).length : 0;
      }
    }
    db.close();
    samples.sort((a, b) => a - b);
    return {
      median: Math.round(samples[3]), min: Math.round(samples[0]), max: Math.round(samples[6]),
      rows, bytes,
    };
  }, { pid: projectId, tid: taskId });
}

interface Seeded { projectId: string; taskId: string }

/** The row ids this browser holds for the task, newest last, plus the newest one's shape. */
async function cachedWindow(page: Page, projectId: string, taskId: string) {
  return page.evaluate(async ({ pid, tid }) => {
    const db: IDBDatabase = await new Promise((res, rej) => {
      const req = indexedDB.open('kanban-storage');
      req.onsuccess = () => res(req.result);
      req.onerror = () => rej(req.error);
    });
    const entry: any = await new Promise((res, rej) => {
      const tx = db.transaction('board-cache', 'readonly');
      const req = tx.objectStore('board-cache').get(`messages:${pid}:${tid}`);
      req.onsuccess = () => res(req.result);
      req.onerror = () => rej(req.error);
    });
    db.close();
    const rows: any[] = entry?.data?.messages ?? [];
    return { ids: rows.map(r => r.id), roles: rows.map(r => r.role), hasMore: !!entry?.data?.hasMore };
  }, { pid: projectId, tid: taskId });
}

/** The newest window the server answers, same shape. */
async function serverWindow(page: Page, projectId: string, taskId: string) {
  return page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`, { cache: 'no-store' });
    const rows: any[] = await res.json();
    return {
      ids: rows.map(r => r.id), roles: rows.map(r => r.role),
      hasMore: res.headers.get('X-Messages-More') === 'true',
    };
  }, { pid: projectId, tid: taskId });
}

/** The task's active thread, which is what a `?thread=` open names. */
async function activeThread(page: Page, projectId: string, taskId: string): Promise<string> {
  const threads: any[] = await page.evaluate(async ({ pid, tid }) =>
    (await fetch(`/api/projects/${pid}/tasks/${tid}/threads`)).json(), { pid: projectId, tid: taskId });
  const active = threads.find(t => t.is_active) ?? threads[0];
  expect(active, 'the task has no thread to open').toBeTruthy();
  return active.id as string;
}

async function makeTaskWithHistory(page: Page, rows = SEED_ROWS, body?: string): Promise<Seeded> {
  await gotoProject(page);
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: { title: `open-timing-${Date.now()}`, prompt: 'open timing fixture', status: 'in_progress' },
  });
  expect(created.ok, `create task: ${JSON.stringify(created.data)}`).toBe(true);
  const taskId: string = created.data.id;
  await seedConversation(page, projectId, taskId, rows, body);
  return { projectId, taskId };
}

async function openTask(page: Page, projectId: string, taskId: string, timeout = 30000, query = ''): Promise<Harvest> {
  // Resource timing cannot say which answers were 304s, and on a capped link that is the difference
  // between paying a round trip and paying for the body.
  const revalidated: string[] = [];
  const onResponse = (res: { status: () => number; url: () => string }) => {
    const path = new URL(res.url()).pathname;
    if (path.startsWith('/api/')) revalidated.push(`${res.status()} ${path.split('/tasks/')[1] ? path.replace(/^.*\/tasks\//, 'task/').replace(/\/[a-z0-9]{6,}/g, '…') : path}`);
  };
  page.on('response', onResponse);
  try {
    await page.goto(`/?project=${projectId}&task=${taskId}${query}`, { timeout });
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout });
    await expect(page.locator('.chat-messages .chat-entry[data-entry-id]').first())
      .toBeVisible({ timeout: 20000 });
    await expect.poll(async () => (await page.evaluate(() => (window as any).__probe.done)),
      { timeout: 20000 }).toBe(true);
    const h = await harvest(page);
    h.revalidated = revalidated.filter(s => s.startsWith('304'));
    return h;
  } finally {
    page.off('response', onResponse);
  }
}

/** What the two candidate fixes could hand back, measured under the condition being reported: the chain
 * asked in parallel (what unchaining `onMount` does), and this task's cached window read from IndexedDB
 * (what a provisional paint starts from). A paint from the cache waits on no read at all, so its ceiling
 * is the whole post-mount wait minus its own cost, not just the window read's. */
async function annotateLevers(page: Page, projectId: string, taskId: string, h: Harvest, label: string,
  measureChain = true) {
  const chain = measureChain
    ? await chainCost(page, projectId, taskId)
    : { serial: 0, parallel: 0, one: 0 };
  const cached = await cacheReadCost(page, projectId, taskId);
  const postMount = h.firstRowAt - h.firstChatAt;
  annotate(`${label}: after the chat mounts it waits ${postMount}ms for its first row. The same three `
    + `reads cost ${chain.serial}ms in series and ${chain.parallel}ms in parallel (unchaining hands back `
    + `${chain.serial - chain.parallel}ms); the cached window is ${cached.rows} rows in ${cached.median}ms `
    + `of IndexedDB read, so a provisional paint waits on none of them and hands back at most `
    + `${postMount - cached.median}ms — and cannot paint before the chat mounts at +${h.firstChatAt}ms, `
    + `which is ${Math.round(h.firstChatAt / h.firstRowAt * 100)}% of the open`
    + (measureChain ? `. The window read on its own is ${chain.one}ms` : '') + ')');
  return { chain, cached };
}

/** What the reader is actually left holding when a read does not answer. */
async function screenState(page: Page) {
  return page.evaluate(() => ({
    entries: document.querySelectorAll('.chat-messages .chat-entry[data-entry-id]').length,
    notice: document.querySelector('.history-notice')?.textContent?.trim() ?? '',
    retry: !!document.querySelector('.history-notice-retry'),
    spinner: !!document.querySelector('.chat-loading .spinner'),
    mirror: !!document.querySelector('.offline-btn'),
  }));
}

test.describe('Where a task open spends its time', () => {
  test('a cold open: one round trip of reads before the first row', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page);
    // One init script covers every navigation this test makes.
    await installProbe(page);

    const cold = await openTask(page, projectId, taskId);
    report('cold open', cold);

    // Nothing sits in front of the window read: threads, the row and the window go out together, so the
    // first painted row waits on one round trip and the render.
    const messages = cold.calls.find(c => c.path === '/api/…/task/messages');
    const threads = cold.calls.find(c => c.path === '/api/…/task/threads');
    const row = cold.calls.find(c => c.path === '/api/…/task');
    expect(messages && threads && row, 'the open did not make all three reads').toBeTruthy();
    expect(messages!.start, 'a read is still serial in front of the window read')
      .toBeLessThanOrEqual(Math.min(threads!.end, row!.end) + 5);
    expect(cold.firstRowAt, 'a row painted before the window was read').toBeGreaterThan(messages!.end - 5);

    // The boot behind it: one session probe — a safe method asks for no CSRF token — and the counts read
    // beside the project list rather than behind the board it has nothing to do with.
    const session = cold.calls.filter(c => c.path === '/api/auth/session');
    const counts = cold.calls.find(c => c.path === '/api/projects/counts');
    const board = cold.calls.find(c => c.path === '/api/…/board');
    expect(session.length, `the boot spent ${session.length} reads on the session`).toBe(1);
    expect(counts && board, 'the boot never read the counts or the board').toBeTruthy();
    expect(counts!.start, 'the counts read is behind the board it does not need')
      .toBeLessThanOrEqual(board!.start + 5);

    // Second open of the same task: IndexedDB now holds the window this read wrote.
    await page.waitForTimeout(500);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const warm = await openTask(page, projectId, taskId);
    report('warm open (cached window in IndexedDB)', warm);

    const costs = await endpointCosts(page, projectId, taskId);
    annotate('single reads, warm and uncontended (median of 7): '
      + Object.entries(costs).map(([n, c]) => `${n}=${c.server}ms/${c.bytes}B`).join(' '));

    await annotateLevers(page, projectId, taskId, warm, 'warm open');
  });

  test('an open that names a thread reads its window after the switch', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page, 10);
    const thread = await activeThread(page, projectId, taskId);
    await installProbe(page);

    const named = await openTask(page, projectId, taskId, 30000, `&thread=${thread}`);
    report('open naming a thread', named);

    // The window read the open began with answers for the thread this screen has just left, so the one that
    // paints is the one that goes out behind the switch.
    const switched = named.calls.find(c => c.path === '/api/…/task/threads/switch');
    const windows = named.calls.filter(c => c.path === '/api/…/task/messages');
    expect(switched, 'an open naming a thread never switched').toBeTruthy();
    expect(windows.length, 'no window read followed the switch').toBeGreaterThan(1);
    expect(windows[windows.length - 1].start, 'the open drew the window of the thread it left')
      .toBeGreaterThanOrEqual(switched!.end - 5);
  });

  test('a task row that does not answer still leaves the reader the rows', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page, 10);
    await installProbe(page);
    // The row is the read that names `execution_state`. Losing it costs the reading of that field — the
    // board's copy stands in — and never the conversation the window carried.
    await page.route(`**/api/projects/${projectId}/tasks/${taskId}`, route =>
      route.request().method() === 'GET' ? route.abort('failed') : route.fallback());

    const noRow = await openTask(page, projectId, taskId);
    report('open with no task row', noRow);
    expect(noRow.rows, 'the transcript stayed empty although the window answered').toBeGreaterThan(0);
    const rowReads = noRow.calls.filter(c => c.path === '/api/…/task');
    expect(rowReads.length, 'the open never asked for the row').toBeGreaterThan(0);
  });

  test('a slow window read: a cached window that can still move paints nothing', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page);
    await installProbe(page);
    // Open once with nothing held back, so the browser holds this task's window in IndexedDB.
    await openTask(page, projectId, taskId);
    const cached = await cacheReadCost(page, projectId, taskId);
    expect(cached.rows, 'the first open never cached a window, so this run proved nothing').toBeGreaterThan(0);

    // Hold the newest-window read, the way a slow uplink or a contended server would.
    const STALL = 1500;
    await page.route(url => url.pathname.endsWith('/messages') && !url.searchParams.has('before'),
      async route => {
        await new Promise(resolve => setTimeout(resolve, STALL));
        await route.continue();
      });
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const stalled = await openTask(page, projectId, taskId);
    report(`warm open, window read stalled ${STALL}ms`, stalled);
    const messages = stalled.calls.find(c => c.path === '/api/…/task/messages');
    expect(messages, 'the open never read the window').toBeTruthy();
    annotate(`stalled warm open: ${cached.rows} cached rows on disk, first row still at `
      + `+${stalled.firstRowAt}ms against a read that ended at +${messages!.end}ms — the wait is the `
      + 'read, not the render. The cache is read on the way in and paints nothing: every seeded row is a '
      + 'user row, so this window holds a send nothing has answered');
    expect(stalled.firstRowAt, 'the row painted before the stalled read returned').toBeGreaterThan(messages!.end);
  });

  test('a server one round trip away: what each read in the chain costs', async ({ page }) => {
    test.setTimeout(180_000);
    const RTT = 150;
    const { projectId, taskId } = await makeTaskWithHistory(page);
    await installProbe(page);
    // Warm the cache first, on a fast link, so the throttled open is the warm case a returning reader has.
    await openTask(page, projectId, taskId);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await throttle(page, { rtt: RTT });

    const throttled = await openTask(page, projectId, taskId);
    report(`warm open at ${RTT}ms RTT`, throttled);
    await annotateLevers(page, projectId, taskId, throttled, `open at ${RTT}ms RTT`);
    expect(throttled.firstRowAt).toBeGreaterThan(0);
  });

  test('a slow cell link with a fat window: what the body actually costs', async ({ page }) => {
    test.setTimeout(240_000);
    // A late-3G profile: 400 kbps down, 100 kbps up, 300 ms round trip.
    const LINK = { rtt: 300, downKbps: 400, upKbps: 100 };
    const { projectId, taskId } = await makeTaskWithHistory(page, 12, fatBody());
    await installProbe(page);
    await openTask(page, projectId, taskId);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await throttle(page, LINK);

    const slow = await openTask(page, projectId, taskId, 90000);
    report('warm open on a 400 kbps / 300 ms link', slow);
    const win = slow.calls.find(c => c.path === '/api/…/task/messages');
    expect(win, 'the open never read the window').toBeTruthy();
    annotate(`fat window on a capped link: the window read took ${win!.end - win!.start}ms — `
      + `${win!.responseStart - win!.requestStart}ms of server, ${win!.end - win!.responseStart}ms of transfer `
      + `for ${win!.bytes} B compressed (${win!.wire} B actually sent). The render after it is `
      + `${slow.firstRowAt - win!.end}ms, and the same rows read out of IndexedDB cost no wire time at all`);
    await annotateLevers(page, projectId, taskId, slow, 'open on a 400 kbps link', false);
    expect(slow.firstRowAt).toBeGreaterThan(win!.end);

    // One row lands, so the next read is a different window. Its size proves nothing: the window holds five
    // rows, and appending one drops the oldest, so the payload moves by the difference between two rows.
    // What this asserts is what its title says. The read carried a body, and on a link this slow the open
    // waits behind that body rather than behind the server.
    await seedConversation(page, projectId, taskId, 1, 'one row more #i#');
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const moved = await openTask(page, projectId, taskId, 90000);
    const movedWin = moved.calls.find(c => c.path === '/api/…/task/messages');
    expect(movedWin, 'the second open never read the window').toBeTruthy();
    annotate(`the same read after one row arrived: ${movedWin!.bytes} B compressed, ${movedWin!.wire} B `
      + `actually sent, ${movedWin!.end - movedWin!.start}ms (the read before it: ${win!.bytes} B, `
      + `${win!.wire} B sent, ${win!.end - win!.start}ms)`);
    expect(movedWin!.bytes, 'the moved thread was answered with no body').toBeGreaterThan(0);
    const transfer = movedWin!.end - movedWin!.responseStart;
    const serverTime = movedWin!.responseStart - movedWin!.requestStart;
    expect(transfer, `the body's transfer (${transfer}ms) is what the open waits behind, against `
      + `${serverTime}ms of server`).toBeGreaterThan(serverTime);
  });

  test('what a provisional paint would show: how far the cached window lags', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page);
    await installProbe(page);
    await openTask(page, projectId, taskId);
    const held = await cachedWindow(page, projectId, taskId);

    // The conversation moves while this browser is not looking — the case a provisional paint gets wrong.
    const MOVED = 3;
    await seedConversation(page, projectId, taskId, MOVED, `arrived while away #i#`);
    const live = await serverWindow(page, projectId, taskId);

    const heldIds = new Set(held.ids);
    const liveIds = new Set(live.ids);
    const missing = live.ids.filter(id => !heldIds.has(id)).length;
    const stale = held.ids.filter(id => !liveIds.has(id)).length;
    annotate(`cached window ${held.ids.length} rows vs the server's ${live.ids.length}: `
      + `${live.ids.length - missing} of the newest window already on disk, ${missing} arrived while away, `
      + `${stale} cached rows no longer in the newest window; newest cached row is a `
      + `"${held.roles[held.roles.length - 1]}" against the server's "${live.roles[live.roles.length - 1]}"; `
      + `hasMore cached=${held.hasMore} live=${live.hasMore}`);
    expect(missing, 'the seeded rows never arrived, so this run proved nothing').toBe(MOVED);
  });

  test('a window read that never answers: what the reader is left holding', async ({ page }) => {
    test.setTimeout(180_000);
    const { projectId, taskId } = await makeTaskWithHistory(page);
    await installProbe(page);
    await openTask(page, projectId, taskId);
    const cached = await cacheReadCost(page, projectId, taskId);
    expect(cached.rows, 'the first open cached no window, so this run proved nothing').toBeGreaterThan(0);

    let dropped = 0;
    await page.route(url => url.pathname.endsWith('/messages') && !url.searchParams.has('before'),
      async route => { dropped++; await route.abort('failed'); });
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const at = Date.now();
    await page.goto(`/?project=${projectId}&task=${taskId}`, { timeout: 60000 });
    // Whatever the app settles into — a mirrored transcript or a notice — is the answer being measured.
    await expect.poll(async () => {
      const s = await screenState(page);
      return s.entries + (s.notice ? 1 : 0);
    }, { timeout: 30000 }).toBeGreaterThan(0);
    const waited = Date.now() - at;
    const state = await screenState(page);
    annotate(`dropped window read (${cached.rows} cached rows on disk): ${state.entries} rows on screen `
      + `${waited}ms after the navigation; notice="${state.notice}" retry=${state.retry} `
      + `spinner=${state.spinner} mirror=${state.mirror}`);
    expect(dropped, 'no read was dropped, so this run proved nothing').toBeGreaterThan(0);
    // A read that failed leaves the mirrored rows up and says where they came from. What the open draws from
    // the cache on its own account is only a Review result (`provisional-review-paint.spec.ts`); a window
    // that can still move reaches this screen through a failed read and its band.
    expect(state.entries, 'a dropped window read left the reader an empty transcript').toBeGreaterThan(0);
    expect(state.notice, 'mirrored rows were shown without saying they may be stale').not.toBe('');
  });

  test('a boot the link cannot answer in two seconds: the mirror offer, not the transcript', async ({ page }) => {
    test.setTimeout(240_000);
    const LINK = { rtt: 400, downKbps: 400, upKbps: 100 };
    const { projectId, taskId } = await makeTaskWithHistory(page);
    await installProbe(page);
    await openTask(page, projectId, taskId);
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await throttle(page, LINK);

    await page.goto(`/?project=${projectId}&task=${taskId}`, { timeout: 90000, waitUntil: 'commit' });
    const mirror = await page.locator('.offline-btn').waitFor({ timeout: 20000 }).then(() => true).catch(() => false);
    const rows = await page.locator('.chat-messages .chat-entry[data-entry-id]').first()
      .waitFor({ timeout: 20000 }).then(() => true).catch(() => false);
    const h = await harvest(page);
    annotate(`open on a 400 kbps / 400 ms link: task transcript ${rows ? 'painted' : 'never painted'} `
      + `without a click; offline mirror offered ${h.mirrorAt ? `at +${h.mirrorAt}ms` : 'not at all'}; `
      + `chat shell ${h.firstChatAt ? `+${h.firstChatAt}ms` : 'never'}; mirror button on screen=`
      + `${mirror}`);
    // Either the reader sees the task, or the app offers the mirror. Silence is the failure.
    expect(rows || mirror, 'the slow boot left the reader with neither the task nor an offer').toBe(true);
  });
});
