import { expect, test, Page, Locator, Browser, BrowserContext, TestInfo } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync, readFileSync, cpSync } from 'fs';
import { dirname, join } from 'path';
import https from 'node:https';
import { laneRequestOptions } from '../lane-tls';
import { readSandboxVerdict } from '../sandbox-verdict';

/**
 * Skip the calling test unless this host can start a gVisor container. Only a spec that starts one may
 * call it: a spec that merely reads sandbox settings never reaches podman and must still run.
 */
export function requireSandbox(): void {
  const verdict = readSandboxVerdict();
  test.skip(!verdict.usable,
    `sandbox is not fully supported in this environment — ${verdict.reason}; specs that start a container are skipped`);
}

/**
 * Find a provider by its reference string: the stable integer ID (as a string)
 * for LLM providers, or the provider name for agent providers.
 */
export function findProviderByRef(providers: any[], ref: string): any | undefined {
  return providers.find((p: any) =>
    p.kind === 'agent' || (p.url || '').startsWith('agent://')
      ? p.name === ref
      : String(p.id) === ref || p.name === ref
  );
}

// --- Shared API helpers ---

/** Get the server origin from the page, navigating first if needed. */
async function getOrigin(page: Page): Promise<string> {
  const url = page.url();
  if (!url || url === 'about:blank') {
    await page.goto('/');
  }
  return await page.evaluate(() => window.location.origin);
}

/**
 * Get CSRF token from page context via the authenticated session endpoint
 */
async function getCsrfToken(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    try {
      const res = await fetch('/api/auth/session');
      const data = await res.json();
      return data.csrf_token || '';
    } catch {
      return '';
    }
  });
}

/**
 * CSRF-aware fetch for use inside page.evaluate. A raw fetch on a write method is refused.
 */
export async function csrfFetch(
  page: Page,
  url: string,
  options: { method: string; body?: Record<string, any> } = { method: 'POST' }
): Promise<any> {
  await getOrigin(page);
  const csrf = await getCsrfToken(page);
  return await page.evaluate(async ({ apiUrl, opts, csrfToken }) => {
    const res = await fetch(apiUrl, {
      method: opts.method,
      headers: {
        'Content-Type': 'application/json',
        ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      },
      credentials: 'include',
      body: opts.body ? JSON.stringify(opts.body) : undefined,
    });
    const data = await res.json().catch(() => null);
    return { ok: res.ok, status: res.status, data };
  }, { apiUrl: url, opts: options, csrfToken: csrf });
}

/** PUT /api/config with the given body and wait for it to propagate. */
export async function apiPutConfig(page: Page, body: Record<string, any>): Promise<any> {
  await getOrigin(page);
  const csrfToken = await getCsrfToken(page);
  return await page.evaluate(async ({ bodyData, csrf }) => {
    const res = await fetch('/api/config', {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
      },
      credentials: 'include',
      body: JSON.stringify(bodyData),
    });
    if (!res.ok) return null;
    return res.json();
  }, { bodyData: body, csrf: csrfToken });
}

/** GET /api/config and return parsed JSON. */
export async function apiGetConfig(page: Page): Promise<any> {
  await getOrigin(page);
  return await page.evaluate(async () => {
    const res = await fetch('/api/config');
    if (!res.ok) return null;
    return res.json();
  });
}
/**
 * The session file for the lane this test runs in. A spec that creates its own context must use
 * this: the lanes are different servers, and the wrong file means every API call gets a 401.
 */
export function storageStatePath(projectName: string): string {
    return projectName === 'isolated'
        ? '/tmp/kanban-test-storage-state.json.isolated'
        : '/tmp/kanban-test-storage-state.json';
}

/**
 * A page signed in with a lane session, for the `beforeAll`/`afterAll` a `page` fixture cannot reach (the
 * fixture is not available there, so the spec opens its own context). `browser.newContext()` already
 * inherits the project's `use` — `storageState` and `baseURL` included — so naming them here is not what
 * signs the page in; it is what a caller off its own lane says (an isolated spec reaching 3001 from a
 * chromium test, where the wrong file means every API call answers 401). Pass `landing: null` for a tab
 * whose first navigation the test itself performs.
 */
export async function signedInPage(
    browser: Browser,
    { storageState, baseURL, landing = '/' }:
        { storageState?: string; baseURL?: string; landing?: string | null } = {},
): Promise<{ context: BrowserContext; page: Page }> {
    const context = await browser.newContext({
        ...(storageState ? { storageState } : {}),
        ...(baseURL ? { baseURL } : {}),
    });
    const page = await context.newPage();
    if (landing) await page.goto(landing);
    return { context, page };
}

/** GET a JSON endpoint from the page (the page must be on the server origin). */
export async function apiGetJson<T>(page: Page, path: string): Promise<T> {
  await getOrigin(page);
  return await page.evaluate(async (p) => {
    const res = await fetch(p);
    return res.ok ? await res.json() : null;
  }, path) as T;
}

/** Wait until the server's config matches `predicate`. */
export async function waitForConfig(page: Page, predicate: (config: any) => boolean, timeout = 2000): Promise<void> {
  await expect(async () => {
    const config = await apiGetConfig(page);
    expect(predicate(config)).toBe(true);
  }).toPass({ timeout });
}

// --- Node-side server API helper (works while the browser context is offline) ---

/**
 * A server API request from the Node test process, authenticated from the lane's session file, so it works
 * while the browser context is offline or mid-reload. `base` is the lane's origin.
 */
/**
 * A server API request from the Node test process, authenticated from the lane's session file, so it works
 * while the browser context is offline or mid-reload. `base` is the lane's origin.
 */
export async function serverApi(
  base: string,
  path: string,
  options: { method?: string; body?: Record<string, any> } = {},
): Promise<{ ok: boolean; status: number; data: any }> {
  const port = new URL(base).port;
  const authFile = port === '3001' ? '/tmp/kanban-test-auth.json.isolated' : '/tmp/kanban-test-auth.json';
  const auth = JSON.parse(readFileSync(authFile, 'utf-8'));
  const cookie = auth.cookies?.[0]?.value ?? '';
  const csrfToken = auth.csrfToken ?? '';
  const method = options.method ?? 'GET';
  const body = options.body ? JSON.stringify(options.body) : undefined;
  return new Promise((resolve, reject) => {
    const req = https.request(
      {
        host: 'localhost',
        port: Number(port),
        path,
        method,
        ...laneRequestOptions(port),
        headers: {
          'Content-Type': 'application/json',
          Cookie: `PHPSESSID=${cookie}`,
          ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
        },
      },
      (res) => {
        let raw = '';
        res.on('data', (chunk: Buffer) => raw += chunk);
        res.on('end', () => {
          let data: any = null;
          try { data = JSON.parse(raw); } catch { data = raw; }
          const status = res.statusCode ?? 0;
          resolve({ ok: status >= 200 && status < 300, status, data });
        });
      },
    );
    req.on('error', reject);
    if (body) req.write(body);
    req.end();
  });
}

// --- Project & Task API helpers ---

/**
 * The project this worker owns, when a spec has claimed one (see isolatedProject). Module state
 * is per worker, which is the granularity the project is keyed at.
 */
let ownProject: { id: string; path: string } | null = null;

/** Get the first project ID from the API — or this worker's own project, if claimed. */
export async function getProjectId(page: Page): Promise<string> {
  if (ownProject) return ownProject.id;
  const projects = await apiGetJson<any[]>(page, '/api/projects');
  // Pin the global-setup project by path: with per-worker projects on the same server,
  // projects[0] is simply whichever was created first.
  return (projects || []).find((p: any) => p.path === FIXTURE_REPO)?.id || projects?.[0]?.id || '';
}

/**
 * The run's long transcript, built once by global setup. A spec that measures a reader's scroll reads it
 * rather than POSTing rows of its own.
 */
export function chatFixture(): { projectId: string; taskId: string; rows: number } {
  const file = '/tmp/kanban-test-chat-fixture.json';
  if (!existsSync(file)) {
    throw new Error(`chatFixture: ${file} is missing — global setup builds the transcript with the run's projects`);
  }
  return JSON.parse(readFileSync(file, 'utf-8'));
}

/**
 * Change a card's title through the edit dialog and wait for the dialog to close. The board moves when it
 * closes; whether the server took the edit is a separate fact.
 */
export async function editTaskTitle(page: Page, taskId: string, title: string): Promise<void> {
  const card = page.locator(`section[aria-label="Kanban board"] .card[data-task-id="${taskId}"]`);
  await card.hover();
  await card.locator('[aria-label="Edit task"]').click();
  await page.fill('#edit-task-title-input', title);
  await page.locator('.modal-footer .btn-primary').click();
  await expect(page.locator('#edit-task-title-input')).toHaveCount(0);
}

/**
 * Write rows straight into a task's thread: `POST …/messages` stores a row without running a turn, which is
 * how a spec gets a transcript tall enough that "the top" and "the bottom" are hundreds of pixels apart.
 * One row per request, oldest first, so a row's position is the order they were written.
 *
 * A fixture a spec builds, not one it measures inside: a conversation a spec *measures* comes from
 * `chatFixture()`, because 200 sequential POSTs inside a test time out on a server every worker queues
 * turns on (`docs/testing.md → Concurrency is part of the environment`). These specs seed a few dozen rows
 * and measure the reader, not the rows.
 */
export async function seedChatRows(
  page: Page, projectId: string, taskId: string, rows: number, label: string,
): Promise<void> {
  await getOrigin(page);
  const token = await getCsrfToken(page);
  const seeded = await page.evaluate(async ({ url, count, token, label }) => {
    let made = 0;
    for (let i = 0; i < count; i++) {
      const res = await fetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', ...(token ? { 'X-CSRF-Token': token } : {}) },
        credentials: 'include',
        body: JSON.stringify({
          role: 'user',
          // The row opens with a token no other row carries, so the screen can be asked which row it is
          // showing — a row identified by its position in a window is somebody else's row.
          content: `${label}-${i}\n${'filler text that gives the entry some height '.repeat(3)}`,
        }),
      });
      if (!res.ok) return { made, status: res.status, text: await res.text() };
      made++;
    }
    return { made, status: 200, text: '' };
  }, {
    url: `/api/projects/${projectId}/tasks/${taskId}/messages`,
    count: rows,
    token,
    label,
  });
  if (seeded.status !== 200) {
    throw new Error(`seedChatRows: stopped at row ${seeded.made} with ${seeded.status}: ${seeded.text}`);
  }
}

// --- The browser's own storage ---
//
// `storage.ts` owns this database, its stores and the shape of what it writes; these are the only reads a
// spec needs from the other side of that boundary. The value comes back exactly as stored — a `board-cache`
// entry is the `{ data }` wrapper the app wrote, a `drafts` entry is the text itself — so a spec asserting
// the screen drew something and a spec asserting the browser kept it are two different claims.

/** One transaction against `storage.ts`'s database. Every spec-side read goes through here. */
async function storageOp<T>(
  page: Page, op: 'get' | 'keys' | 'put' | 'delete', store: string, key = '', value?: unknown,
): Promise<T | null> {
  return page.evaluate(async ([op, storeName, k, v]) => {
    const db: IDBDatabase = await new Promise((resolve, reject) => {
      const rq = indexedDB.open('kanban-storage');
      rq.onsuccess = () => resolve(rq.result);
      rq.onerror = () => reject(rq.error);
    });
    try {
      const objectStore = db
        .transaction(storeName, op === 'get' || op === 'keys' ? 'readonly' : 'readwrite')
        .objectStore(storeName);
      return await new Promise<any>((resolve, reject) => {
        const req = op === 'get' ? objectStore.get(k)
          : op === 'keys' ? objectStore.getAllKeys()
            : op === 'put' ? objectStore.put(v, k)
              : objectStore.delete(k);
        const tx = req.transaction!;
        tx.onerror = () => reject(tx.error);
        if (op === 'get') req.onsuccess = () => resolve(req.result === undefined ? null : req.result);
        else if (op === 'keys') req.onsuccess = () => resolve(req.result);
        else tx.oncomplete = () => resolve(null);
      });
    } finally {
      db.close();
    }
  }, [op, store, key, value] as [string, string, string, unknown]);
}

/** What this browser holds under `key`, or `null` when it holds nothing there. */
export const readStored = <T,>(page: Page, store: string, key: string) =>
  storageOp<T>(page, 'get', store, key);

/** Whether the store holds any key under `prefix`, for a key whose tail the spec cannot name. */
export async function hasStoredKeyStartingWith(page: Page, store: string, prefix: string): Promise<boolean> {
  const keys = await storageOp<string[]>(page, 'keys', store);
  return (keys ?? []).some(k => String(k).startsWith(prefix));
}

/** Write a value the app reads back at boot — a settings choice, mostly. */
export const writeStored = (page: Page, store: string, key: string, value: unknown) =>
  storageOp(page, 'put', store, key, value);

/** Drop a key: the cleanup a spec owes the next one after it filled a draft store. */
export const deleteStored = (page: Page, store: string, key: string) =>
  storageOp(page, 'delete', store, key);

// --- Settings ---

/**
 * Open Settings on one of its tabs and wait until that tab is the active one. The wait is on the tab button,
 * never on `[role="dialog"]`: that role names the Edit dialogs opened over Settings as well, so it is
 * answered while the panel the caller wants does not exist yet. `tab` is the button's label — the project
 * tab's label is the project's name.
 */
export async function openSettingsTab(page: Page, tab: string): Promise<void> {
  await page.waitForSelector('.add-task-btn, .icon-btn[aria-label="Settings"]', { timeout: 10000 });
  await page.locator('.icon-btn[aria-label="Settings"]').first().click();
  const button = page.locator(`button.tab-btn:has-text("${tab}")`);
  await button.click();
  await expect(button).toHaveClass(/\bactive\b/);
}

/**
 * How many writes the persisted queue holds. A value drawn on screen proves nothing about the queue, and a
 * request still running when the page went away never became an entry.
 */
export async function queuedActionCount(page: Page): Promise<number> {
  const actions = await readStored<unknown[]>(page, 'offline-queue', 'actions');
  return Array.isArray(actions) ? actions.length : 0;
}

/**
 * End an offline stretch and wait for the browser's offline flag to clear. A page that boots while it still
 * reads offline holds the queue for an `online` event that already fired.
 */
export async function goOnline(page: Page): Promise<void> {
  await page.context().setOffline(false);
  await expect.poll(() => page.evaluate(() => navigator.onLine), { timeout: 10000 }).toBe(true);
}

// --- Isolated project per worker ---
//
// isolatedProject() gives the current worker its own copy of the fixture repo, so specs do not change one
// board at once. Keyed by worker, not file: fullyParallel splits one file across workers, and two workers
// would then claim the same path.
//
// Call it once at file top level, then gotoProject(page). A spec that tests the shared project list (sidebar,
// add-project flow) must not use it.

const OWN_REPOS_DIR = '/tmp/kanban-test-repos';
const FIXTURE_REPO = '/tmp/kanban-test-repo';

export function isolatedProject(): void {
  test.beforeEach(async ({ page }, testInfo) => {
    // The port keeps the two lanes' slots disjoint (3000 chromium, 3001 isolated).
    const base = (testInfo.project.use as { baseURL?: string })?.baseURL || 'https://localhost:3000';
    const own = await ensureOwnProject(page, `w${testInfo.parallelIndex}-${new URL(base).port}`);
    // Re-seeded per test, not once per file: a file in parallel mode runs its `afterAll` between groups of
    // its own tests, so the teardown below can empty this project while the file still has tests left.
    await seedIfEmpty(page, own.id);
  });

  // A file that owns a project leaves it empty: the project outlives the file, so a leftover card is one the
  // next file finds in a column it did not put it in. Deleting a task is also what stops its container.
  test.afterAll(({ browser }, testInfo) => emptyOwnProject(browser, ownStorageState(testInfo)));
}

/**
 * Release this worker's project between tests rather than only at the end of the file.
 *
 * A live task is charged against the executor's memory budget for as long as it exists — every container it
 * owns at `max(held, cap)`, its cap `types.DefaultSandboxMemoryLimitMB` (1024 MB) for every project this
 * harness creates — and one capacity covers every worker on the lane server. Measured consequence: a file that
 * starts one turn per test and releases nothing had 13 of its own containers alive against a 14379 MB capacity,
 * and the log from that moment reads `running 0, queued 3` until the file ends. A turn parked in that queue
 * issues no LLM call and renders nothing, so no wait in the spec that sent it outlasts the leak: whichever
 * turn-bound wait burns its budget next reports the leak, in whichever spec it happens to live in.
 * `docs/sandbox-runtime.md → The memory budget` owns the rule; `docs/testing.md → E2E conventions` the rest.
 */
export function clearTasksAfterEach(): void {
  test.afterEach(({ browser }, testInfo) => emptyOwnProject(browser, ownStorageState(testInfo)));
}

/** The storage state this test's project was opened with, so a cleanup page is authed like the test. */
function ownStorageState(testInfo: TestInfo): string | undefined {
  return (testInfo.project.use as { storageState?: string }).storageState;
}

/**
 * Delete every task of this worker's own project, from a page of its own. A refusal is logged rather than
 * thrown, because a silent leftover is what the next file inherits and a throw in teardown fails the wrong test.
 */
async function emptyOwnProject(browser: Browser, storageState?: string): Promise<void> {
  if (!ownProject) return;
  const context = await browser.newContext(storageState ? { storageState } : {});
  const page = await context.newPage();
  try {
    const refused = await deleteTasksExcept(page, ownProject.id, []);
    if (refused.length > 0) {
      console.log(`[helpers] left ${refused.length} task(s) on project ${ownProject.id}: ${refused.join(', ')}`);
    }
  } finally {
    await context.close();
  }
}

async function ensureOwnProject(page: Page, slot: string): Promise<{ id: string; path: string }> {
  if (ownProject) return ownProject;

  const path = `${OWN_REPOS_DIR}/${slot}`;
  // A re-import in the same worker must reuse the row, not re-POST. apiGetJson, not csrfFetch: that one sends
  // a JSON content-type with no body, which a GET-only route rejects.
  const projects = await apiGetJson<any[]>(page, '/api/projects');
  const existing = (projects || []).find((p: any) => p.path === path)?.id || '';
  if (existing) {
    ownProject = { id: existing, path };
    return ownProject;
  }

  if (!existsSync(`${path}/.git`)) {
    mkdirSync(OWN_REPOS_DIR, { recursive: true });
    // cpSync keeps the fixture repo's .git, including the git identity global setup set for commit-and-done.
    cpSync(FIXTURE_REPO, path, { recursive: true });
  }

  const created = await csrfFetch(page, '/api/projects', { method: 'POST', body: { name: slot, path } });
  const id = created?.data?.id;
  if (!created.ok || !id) {
    throw new Error(`isolatedProject: could not create project at ${path}: ${created.status} ${JSON.stringify(created.data)}`);
  }
  ownProject = { id, path };
  return ownProject;
}

/**
 * Give the project global-setup's two seed tasks, unless it already has tasks. Specs wait for `.card` as the
 * sign the app is up, and a card belongs to whichever spec created one last.
 */
async function seedIfEmpty(page: Page, id: string): Promise<void> {
  const board = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${id}/board`);
  if (Array.isArray(board?.tasks) && board.tasks.length > 0) return;
  const config = await apiGetConfig(page);
  const seed = (prompt: string) => csrfFetch(page, `/api/projects/${id}/tasks`, {
    method: 'POST',
    body: { prompt, llama_server_url: config?.providers?.[0]?.url || '', model_name: config?.worker_model || '' },
  });
  await seed('Test task for e2e tests');
  await seed('Second test task');
}

/** The URL of this file's own project, e.g. `/?project=xyz`. */
function projectUrl(path = '/'): string {
  if (!ownProject) return path;
  if (/[?&]project=/.test(path)) return path;
  const [before, query] = path.split('?');
  return `${before}?project=${ownProject.id}${query ? `&${query}` : ''}`;
}

/**
 * Delete every task whose status is not in `keep`, and name the ones that refused. The two callers judge the
 * result differently — a teardown only logs a refusal, a spec re-reads the board — so the judging stays with
 * them and this only does the deleting.
 */
async function deleteTasksExcept(page: Page, projectId: string, keep: string[]): Promise<string[]> {
  const board = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${projectId}/board`);
  const refused: string[] = [];
  for (const t of (board?.tasks || []).filter((t: any) => !keep.includes(t.status))) {
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${t.id}`, { method: 'DELETE' });
    if (!res.ok) refused.push(`${t.id} (${res.status})`);
  }
  return refused;
}

/**
 * Delete every task whose status is not in `keep`. Some assertions only hold on a sparse
 * board, and a worker's project outlives the spec that filled it.
 */
export async function clearBoardExcept(page: Page, projectId: string, keep: string[] = []): Promise<void> {
  await deleteTasksExcept(page, projectId, keep);
  await expect(async () => {
    const after = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${projectId}/board`);
    expect((after?.tasks || []).filter((t: any) => !keep.includes(t.status))).toHaveLength(0);
  }).toPass({ timeout: 5000 });
}

/** `page.goto` for the project this file owns. */
export async function gotoProject(page: Page, path = '/', options?: Parameters<Page['goto']>[1]): Promise<void> {
  await page.goto(projectUrl(path), options);
}

/**
 * Does a text box that clips itself keep the ink of its own glyphs? `overflow: hidden` paints only inside the
 * line box, so a line box shorter than the font's box cuts every descender, and only a browser can measure
 * that. Answers '' when the text fits, a sentence naming the shortfall when it does not.
 */
export async function descenderProblems(page: Page, selector: string): Promise<string> {
  return page.evaluate((sel) => {
    const el = document.querySelector(sel);
    if (!el) return `no ${sel}`;
    const cs = getComputedStyle(el);
    if (cs.overflow !== 'hidden') return ''; // nothing is being cut
    if (cs.lineHeight === 'normal') return ''; // no line box of its own, so nothing shorter than the glyphs
    const ctx = document.createElement('canvas').getContext('2d')!;
    ctx.font = `${cs.fontStyle} ${cs.fontWeight} ${cs.fontSize} ${cs.fontFamily}`;
    const g = ctx.measureText('g');
    const room = (parseFloat(cs.lineHeight) - g.fontBoundingBoxAscent + g.fontBoundingBoxDescent) / 2;
    return g.actualBoundingBoxDescent > room
      ? `${sel}: "g" paints ${g.actualBoundingBoxDescent}px below the baseline, its `
        + `${cs.lineHeight} line box leaves ${room.toFixed(2)}px`
      : '';
  }, selector);
}
/**
 * Click the sidebar row for `projectId`. `.project-item` + `.first()` picks somebody else's project.
 */
export async function selectProject(page: Page, projectId: string): Promise<void> {
  // Suffix match, not substring: `project=kanban-test-repo` is a prefix of
  // `project=kanban-test-repo-2`, and a substring match there is a strict-mode violation.
  await page.locator(`a.project-item[href$="project=${projectId}"]`).click();
}

/** Run one git command in a directory the spec owns. The e2e lane's fixture repos live outside any
 *  worktree, so this is the harness building its own scenery, not the product's git path. */
export function gitIn(dir: string, args: string): void {
  execSync(`git ${args}`, { cwd: dir, stdio: 'pipe' });
}

/**
 * Open a project's Review Changes panel: board, project selected, explorer open, Changes view on screen.
 * The wait is on the panel's title — a row there is `git status` rendered, and an empty status is a valid
 * answer the panel still has to prove it drew.
 */
export async function openChangesPanel(page: Page, projectId: string): Promise<void> {
  await page.goto('/');
  await selectProject(page, projectId);
  await page.waitForSelector('.top-bar', { timeout: 10000 });
  await page.locator('.explorer-btn').click();
  await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
  await page.locator('.git-status-btn').click();
  await expect(page.locator('.panel-title:has-text("Changes")')).toBeVisible({ timeout: 10000 });
}

/**
 * One task's card inside one column. Unscoped, `.card[data-task-id]` matches the same task twice for
 * the ~200ms a column move takes to draw: the node leaving the old column stays attached through its
 * fade outro while the node arriving in the new column fades in from opacity 0. Name the column, or
 * wait for the board to hold one card before acting on it.
 */
export function cardInColumn(page: Page, taskId: string, columnTitle: string): Locator {
  return page.locator(`.column[aria-label="${columnTitle} column"] .card[data-task-id="${taskId}"]`);
}

/**
 * Clicks a card once the board has stopped animating its move: a card that just changed column stays on
 * screen while the node it left fades out, so `[data-task-id]` matches two elements for a moment.
 */
export async function clickCard(page: Page, taskId: string): Promise<void> {
  const card = page.locator(`.card[data-task-id="${taskId}"]`);
  await expect(card, 'the board holds one node for this task: the move it just made has finished fading')
      .toHaveCount(1);
  await card.click();
  await expect(page.locator('.task-detail-page')).toBeVisible();
}
/**
 * The directory a project points at — what its file browser, Review-Changes panel and
 * project-level git commands read. Not a task worktree: that is `getWorktreePath`.
 */
export async function projectDirPath(page: Page, projectId: string): Promise<string | null> {
  const projects = await apiGetJson<any[]>(page, '/api/projects');
  return (projects || []).find((p: any) => p.id === projectId)?.path || null;
}

/** Host path of this worker's project repo (null if it did not claim one) — the directory
 *  the project explorer lists, i.e. where files written from Node show up in the UI. */
export function ownProjectPath(): string | null {
  return ownProject?.path ?? null;
}

/** This file's own project id (throws if the file did not call `isolatedProject()`). */
export function ownProjectId(): string {
  if (!ownProject) throw new Error('ownProjectId: this spec does not call isolatedProject()');
  return ownProject.id;
}


/** Get task data from the API. */
export async function getTaskData(page: Page, projectId: string, taskId: string): Promise<Record<string, unknown>> {
  return await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.json();
  }, { pid: projectId, tid: taskId });
}

/** Poll the task row (server truth) until the predicate holds. */
export async function waitForTaskState(
  page: Page,
  projectId: string,
  taskId: string,
  predicate: (t: any) => boolean,
  timeout = 60000,
): Promise<void> {
  await expect(async () => {
    const task = await getTaskData(page, projectId, taskId);
    expect(predicate(task)).toBe(true);
  }).toPass({ timeout });
}
// --- The turn: sending one, telling whether it is still running, telling when it ended ---

/** `taskIsInFlight` ($lib/utils/task-utils.ts) for a raw task row. A stopped task keeps `status = in_progress`
 *  and only moves `execution_state`, so the status is never this answer. */
export const notInFlight = (t: any): boolean =>
  t?.execution_state !== 'running' && t?.execution_state !== 'pulling_image';

/**
 * The run is over: the turn left the task in Review and nothing is running. A turn that stopped on its own ends
 * `review` + `idle`, so `execution_state === 'failed'` is not part of this.
 */
export const runHasEnded = (t: any): boolean => t?.status === 'review' && notInFlight(t);

/** The pending tool-call band: a call announced and not yet answered. */
export const PENDING_TOOL_STATUS = '.tool-status.pending';
/** The tool card a pending band belongs to. */
export const TOOL_CALL_CARD = '.tool-call-card';
/** What the `slow-tool-probe` turn prints from inside its bash call. */
export const SLOW_PROBE_OUTPUT = 'SLOW-TOOL-PROBE-DONE';

/**
 * The return harness, installed before the app boots. `stopReading`/`comeBack` flip the two flags; this is
 * what makes a return measurable:
 *
 *   `__unfocused`  — `document.hasFocus()` answers false, which is how the app decides it was unread.
 *   `__away`       — every `/api/stream` frame but the keepalive is dropped before the app sees it, so the
 *                    turn really finishes behind the reader's back. `__swallowed` counts what was taken.
 *   `__signals`    — how many of each return gesture reached the window: a `focus` count that did not move
 *                    means the gesture never arrived, whatever the transcript then did.
 *   `__sync`       — reads of `/sync`.
 *   `__revisions`  — one entry per `/messages` read: the `If-None-Match` the APP passed (a browser
 *                    revalidating its own HTTP cache sends that header too, so only the app's own read
 *                    counts) and `afterSync`, how many digest reads had gone out when it did — which is how
 *                    a spec says "this read followed that digest".
 *   `__mutations`  — DOM changes inside `.chat-content` since `__watchTranscript()` was last called: a read
 *                    that painted and a read that answered with nothing look identical from the wire.
 *
 * A spec that needs a different hook — holding a frame rather than dropping it, tagging who wrote
 * `scrollTop` — installs its own script beside this one; the flags are the same two the flip moves.
 */
export async function watchTheReturn(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const w = window as any;
    w.__away = false;
    w.__unfocused = false;
    w.__swallowed = 0;
    w.__signals = { visibilitychange: 0, focus: 0, pageshow: 0 };
    w.__revisions = [] as Array<{ baseline: string | null; afterSync: number }>;
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
        w.__revisions.push({
          baseline: new Headers(init?.headers).get('If-None-Match'),
          afterSync: w.__sync,
        });
      }
      return realFetch(input as any, init);
    }) as typeof window.fetch;
    /** Count DOM changes inside the transcript, from now until the next call. */
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
    } as typeof WebSocket;
  });
}

/** The window stops being read: it reports no focus, and no frame reaches JavaScript. The spec's own
 *  `addInitScript` installs the hooks this flips; the hooks differ per spec, the flip does not. */
export const stopReading = (page: Page): Promise<void> =>
  page.evaluate(() => { (window as any).__unfocused = true; (window as any).__away = true; });

/** The window comes back. Only `focus` is dispatched — the page was never hidden. */
export const comeBack = (page: Page): Promise<void> => page.evaluate(() => {
  const w = window as any;
  w.__away = false;
  w.__unfocused = false;
  window.dispatchEvent(new Event('focus'));
});

/** A row of a conversation, as the messages route answers it. */
type MessageRow = { id: string; role: string };

/**
 * The rows this task's current thread shows, oldest first. `/messages` answers a bounded window (5 units by
 * default, 25 at most), so a length is not a growth signal and a wait names rows by `id`.
 */
export async function messageRows(page: Page, projectId: string, taskId: string): Promise<MessageRow[]> {
  return (await apiGetJson<MessageRow[]>(
    page, `/api/projects/${projectId}/tasks/${taskId}/messages?limit=25`,
  )) ?? [];
}

/**
 * Send a turn and wait for its answer. The executor answers one turn at a time, so a running turn absorbs the
 * message; `/send` reports a refusal as a status instead of throwing; and rows are matched by id because the
 * window slides. `docs/testing.md → Reading a turn from the store, not from the screen` owns these rules.
 */
export async function sendTurn(
  page: Page, projectId: string, taskId: string, content: string, turnBudget = 60_000,
): Promise<void> {
  const rows = () => messageRows(page, projectId, taskId);
  // The baseline is row identity, not row order: the window slides, so a fast answer can be the newest row
  // before the first read after the send. An unseen `user` row is this send's; a second unseen row is the answer.
  const beforeIds = new Set((await rows()).map(r => r.id));
  await waitForTaskState(page, projectId, taskId, notInFlight, turnBudget);
  const sent = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
    method: 'POST', body: { content },
  });
  if (!sent.ok) {
    throw new Error(`sendTurn: /send refused with ${sent.status}: ${JSON.stringify(sent.data)}`);
  }
  let userRowId = '';
  await expect.poll(async () => {
    const mine = (await rows()).find(r => r.role === 'user' && !beforeIds.has(r.id));
    if (mine) userRowId = mine.id;
    return mine !== undefined;
  }, { timeout: turnBudget, message: 'the sent message never got its own row' }).toBe(true);
  // Something was written after the sent row. Which of an assistant row, a tool call or an error row it was
  // is the spec's question, not this wait's.
  await expect.poll(async () => {
    return (await rows()).some(r => r.role !== 'user' && !beforeIds.has(r.id) && r.id !== userRowId);
  }, { timeout: turnBudget, message: 'the turn wrote nothing' }).toBe(true);
  await waitForTaskState(page, projectId, taskId, notInFlight, turnBudget);
}

/**
 * Idle: the send button is up. Budget for a whole LLM turn, not for a DOM wait — the default
 * 10s expect.timeout is shorter than one queued turn.
 */
export async function waitForSendButton(page: Page, timeout = 45_000): Promise<void> {
  await expect(page.locator('.btn-send')).toBeVisible({ timeout });
}
/** Wait until the chat form answers: send (idle) or stop (running) button visible. */
export async function waitForChatFormReady(page: Page): Promise<void> {
  await expect(page.locator('.chat-form')).toBeVisible();
  // Wait for either the send button (idle) or stop button (LLM responding)
  await expect(async () => {
    const hasSend = await page.locator('.btn-send').isVisible();
    const hasStop = await page.locator('.btn-stop').isVisible();
    expect(hasSend || hasStop).toBe(true);
  }).toPass();
}
/**
 * Send a message through the chat UI and wait for its bubble. The baseline count is read
 * before Enter: reading it after races the optimistic add, and the poll can then never see
 * the count grow.
 *
 * The bubble is found by what it says. A row that was not the reader's can be drawn after it — a turn that
 * loops gets its steering hint stored as a `user` row (`handleLoopDetected`, `backend/exec/loop_helpers.go`)
 * — so the newest `.chat-message.user` is not necessarily the one this typed.
 */
export async function sendMessage(page: Page, message: string): Promise<void> {
  await waitForSendButton(page);
  const chatTextarea = page.locator('textarea[placeholder*="message"]');
  const userMessages = page.locator('.chat-message.user');
  const countBefore = await userMessages.count();

  await chatTextarea.fill(message);
  await chatTextarea.press('Enter');
  await expect(chatTextarea, 'textarea clears on send').toHaveValue('');
  await expect
    .poll(() => userMessages.count(), { message: 'the sent message appears in the chat' })
    .toBeGreaterThan(countBefore);
  const typed = page.locator('.chat-message.user', { hasText: message });
  await expect.poll(() => typed.count(), { message: 'the sent message is the one we typed' })
    .toBeGreaterThan(0);
  await expect(typed.last(), 'the sent message is on screen').toBeVisible();
}

// --- Task lifecycle helpers ---

/**
 * Click "Start Task" if the task is in new state.
 * Private helper used by waitForChatForm.
 */
async function clickStartTaskIfNew(page: Page): Promise<void> {
  const startButton = page.locator('.btn-start-task');
  if (!(await startButton.isVisible().catch(() => false))) {
    return;
  }
  await startButton.click().catch(() => {});
}

/**
 * Move task to in_progress via API (bypasses executor, for UI-only tests).
 * This makes the chat form appear without needing the executor/LLM.
 * Returns true if the task is now in_progress.
 */
export async function ensureTaskStarted(page: Page, projectId: string, taskId: string): Promise<boolean> {
  const taskData = await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.ok ? res.json() : null;
  }, { pid: projectId, tid: taskId });

  if (taskData?.status !== 'backlog') {
    return true;
  }

  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
    method: 'POST',
    body: { status: 'in_progress' },
  });
  if (!result.ok) return false;

  const updated = await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.ok ? res.json() : null;
  }, { pid: projectId, tid: taskId });
  return updated?.status === 'in_progress';
}

/**
 * Wait for the chat form to appear. Handles the case where the "Restart Task"
 * button is shown (executor hasn't responded to initial prompt yet).
 * Returns true if the chat form is visible, false otherwise.
 */
export async function waitForChatForm(page: Page): Promise<boolean> {
  await clickStartTaskIfNew(page);
  const hasFormOrRestart = await page.waitForFunction(() => {
    return document.querySelector('.chat-form') || document.querySelector('.btn-restart-task');
  }).catch(() => false);

  if (!hasFormOrRestart) return false;

  // If restart button is visible, click it to restart the executor
  if (await page.locator('.btn-restart-task').isVisible().catch(() => false)) {
    await page.locator('.btn-restart-task').click();
    await expect(page.locator('.chat-form')).toBeVisible();
    return true;
  }
  await expect(page.locator('.chat-form')).toBeVisible();
  return true;
}

// --- Navigation helpers ---

/**
 * Navigate to a task detail page using window.location (triggers full reload for SPA).
 * page.goto() doesn't re-trigger onMount for SPAs, so we use window.location.href instead.
 */
export async function navigateToTask(page: Page, taskId: string, projectId?: string): Promise<void> {
  if (!taskId) {
    throw new Error(`navigateToTask called with undefined/empty taskId (taskId=${JSON.stringify(taskId)} projectId=${JSON.stringify(projectId)})`);
  }
  // Without an explicit project the app resolves `?task=` against its own default project, which is not this
  // file's project once a spec is isolated.
  const pid = projectId || ownProject?.id;
  const path = pid ? `/?project=${pid}&task=${taskId}` : `/?task=${taskId}`;
  // A page still at `about:blank` resolves no relative href; that happens when a test shares its worker with
  // another test of the same file.
  if (page.url() === 'about:blank') await page.goto(path);
  else await page.evaluate((u) => { window.location.href = u; }, path);
  await expect(page.locator('.task-detail-page')).toBeVisible();
}

// --- Task creation helpers ---

/**
 * Create a task via the API and return its ID. Creation through the UI modal is slower and less reliable.
 */
export async function createTaskViaApi(page: Page, projectId: string, prompt: string): Promise<string> {
  const config = await apiGetConfig(page);
  const worker = findProviderByRef(config?.providers || [], config?.worker_provider);
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: {
      prompt,
      llama_server_url: worker?.url || '',
      model_name: config?.worker_model || '',
    },
  });
  if (!result.ok) {
    throw new Error(`createTaskViaApi: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
  const taskId = result.data?.id;
  if (!taskId) {
    throw new Error('createTaskViaApi: no task ID in API response');
  }
  // The board learns of the task by push or by poll, so the read is waited for.
  await expect(async () => {
    const tasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return await res.json();
    }, projectId);
    expect(tasks.tasks.some((t: any) => t.id === taskId)).toBe(true);
  }).toPass({ timeout: 5000 });
  return taskId;
}
export function getWorktreePath(projectId: string, taskId: string): string {
  return `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
}
/**
 * Wait until the task's worktree is a real git worktree. Moving to in_progress only schedules
 * creation, so writing a fixture earlier mkdirs a bogus path or silently skips the test.
 */
export async function waitForWorktree(projectId: string, taskId: string, timeoutMs = 20000): Promise<string> {
  const path = getWorktreePath(projectId, taskId);
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(path) && existsSync(`${path}/.git`)) return path;
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error(`worktree for task ${taskId} never appeared at ${path}`);
}
/**
 * Wait until the agent has answered the task's prompt. Until then the review bar offers
 * "Resume" instead of "Commit and move to Done" (showResumeButton in TaskChat.svelte).
 */
export async function waitForTurnAnswered(page: Page, projectId: string, taskId: string, timeoutMs = 30000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const msgs = await apiGetJson<any[]>(page, `/api/projects/${projectId}/tasks/${taskId}/messages`).catch(() => [] as any[]);
    const last = (msgs || []).filter((m: any) => m.role !== 'error').slice(-1)[0];
    if (last && last.role !== 'user') return;
    if (Date.now() > deadline) throw new Error(`task ${taskId} never answered its prompt`);
    await new Promise((r) => setTimeout(r, 300));
  }
}

export function worktreeExists(projectId: string, taskId: string): boolean {
  const path = getWorktreePath(projectId, taskId);
  return existsSync(path) && existsSync(`${path}/.git`);
}

/**
 * Put a file in a task's worktree. Staged by default, which is what a commit fixture wants; a spec that
 * measures the panel's view of an UNSTAGED change passes `stage: false` and the write leaves it for git to
 * report as modified.
 */
export function writeFileInWorktree(projectId: string, taskId: string, filePath: string, content: string,
  { stage = true }: { stage?: boolean } = {}): void {
  const worktreePath = getWorktreePath(projectId, taskId);
  const target = join(worktreePath, filePath);
  // The file's own directories, not just the worktree root: a fixture that edits a nested
  // path (`src/lib/utils/format.ts`) would otherwise fail with ENOENT on the write.
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, content);
  if (!stage) return;
  try {
    execSync(`cd ${worktreePath} && git add ${filePath}`);
  } catch {
    // An untracked file is still detected by the diff
  }
}
