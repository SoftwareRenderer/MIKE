import { expect, test, Page, Locator } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync, readFileSync, cpSync, readdirSync } from 'fs';
import { dirname, join } from 'path';
import http from 'node:http';

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

// --- Shared API helpers (replaces duplicate code in individual test files) ---

/** Get the server origin from the page, navigating first if needed. */
async function getOrigin(page: Page): Promise<string> {
  const url = page.url();
  // If page hasn't been navigated yet (about:blank), navigate to get the origin
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
 * CSRF-aware fetch wrapper for use in page.evaluate.
 * Use this instead of raw fetch for POST/PUT/DELETE requests.
 * Returns { ok, status, data } from the response.
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

/** GET a JSON endpoint from the page (the page must be on the server origin). */
export async function apiGetJson<T>(page: Page, path: string): Promise<T> {
  await getOrigin(page);
  return await page.evaluate(async (p) => {
    const res = await fetch(p);
    return res.ok ? await res.json() : null;
  }, path) as T;
}

/** Wait for config to match expected state (replaces blind waitForSettle after PUT). */
export async function waitForConfig(page: Page, predicate: (config: any) => boolean, timeout = 2000): Promise<void> {
  await expect(async () => {
    const config = await apiGetConfig(page);
    expect(predicate(config)).toBe(true);
  }).toPass({ timeout });
}

// --- Node-side server API helper (works while the browser context is offline) ---

/**
 * Make a server API request from the Node test process.
 *
 * Uses the session cookie + CSRF token from /tmp/kanban-test-auth.json.
 * This allows advancing server state (creating tasks, etc.) while the
 * browser WebSocket is down or the page is mid-reload.
 *
 * @param base  Server origin: http://localhost:3000 or http://localhost:3001
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
    const req = http.request(
      {
        host: 'localhost',
        port: Number(port),
        path,
        method,
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

// --- Isolated project per worker ---
//
// Specs used to share global-setup's one project, so ~9 specs changed one board at once and
// assertions on counts, "the first card" or an absent class failed at random: identical code
// differed by 15 failures, and each one passed alone. isolatedProject() gives the current
// worker its own copy of the fixture repo, plus the same two seed tasks global-setup creates.
//
// Keyed by worker, not file: fullyParallel splits one file across workers, and two workers
// would then claim the same path. A worker runs one file at a time, so files never contend.
//
// Usage: isolatedProject() once at file top level, then gotoProject(page) to open it;
// getProjectId(page) returns it too. Specs that assert on the shared project list (sidebar,
// add-project flow) must not use it — the shared view is what they test.

const OWN_REPOS_DIR = '/tmp/kanban-test-repos';
const FIXTURE_REPO = '/tmp/kanban-test-repo';

export function isolatedProject(): void {
  test.beforeEach(async ({ page }, testInfo) => {
    // The port keeps the two lanes' slots disjoint (3000 chromium, 3001 isolated).
    const base = (testInfo.project.use as { baseURL?: string })?.baseURL || 'http://localhost:3000';
    await ensureOwnProject(page, `w${testInfo.parallelIndex}-${new URL(base).port}`);
  });
}

async function ensureOwnProject(page: Page, slot: string): Promise<{ id: string; path: string }> {
  if (ownProject) return ownProject;

  const path = `${OWN_REPOS_DIR}/${slot}`;
  // Idempotent: a re-import in the same worker must reuse, not re-POST.
  // apiGetJson, not csrfFetch: csrfFetch sends a JSON content-type with no body, which the
  // server rejects on a GET.
  const projects = await apiGetJson<any[]>(page, '/api/projects');
  const existing = (projects || []).find((p: any) => p.path === path)?.id || '';
  if (existing) {
    ownProject = { id: existing, path };
    // A previous spec on this worker may have deleted the seed tasks — re-seed so the
    // `waitForSelector('.card')` readiness gate holds for the next file too.
    await seedIfEmpty(page, existing);
    return ownProject;
  }

  if (!existsSync(`${path}/.git`)) {
    mkdirSync(OWN_REPOS_DIR, { recursive: true });
    // cpSync keeps the fixture repo's .git (incl. the local identity global-setup set,
    // which backend commit-and-done commits need).
    cpSync(FIXTURE_REPO, path, { recursive: true });
  }

  const created = await csrfFetch(page, '/api/projects', { method: 'POST', body: { name: slot, path } });
  const id = created?.data?.id;
  if (!created.ok || !id) {
    throw new Error(`isolatedProject: could not create project at ${path}: ${created.status} ${JSON.stringify(created.data)}`);
  }
  ownProject = { id, path };
  await seedIfEmpty(page, id);
  return ownProject;
}

/** Give the project global-setup's two seed tasks, unless it already has tasks. */
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
export function projectUrl(path = '/'): string {
  if (!ownProject) return path;
  if (/[?&]project=/.test(path)) return path;
  const [before, query] = path.split('?');
  return `${before}?project=${ownProject.id}${query ? `&${query}` : ''}`;
}
/**
 * Delete every task whose status is not in `keep`. Some assertions only hold on a sparse
 * board, and a worker's project outlives the spec that filled it.
 */
export async function clearBoardExcept(page: Page, projectId: string, keep: string[] = []): Promise<void> {
  const board = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${projectId}/board`);
  const doomed = (board?.tasks || []).filter((t: any) => !keep.includes(t.status));
  for (const t of doomed) {
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${t.id}`, { method: 'DELETE' });
  }
  await expect(async () => {
    const after = await apiGetJson<{ tasks?: any[] }>(page, `/api/projects/${projectId}/board`);
    expect((after?.tasks || []).filter((t: any) => !keep.includes(t.status))).toHaveLength(0);
  }).toPass({ timeout: 5000 });
}

/** `page.goto` for the project this file owns. */
export async function gotoProject(page: Page, path = '/'): Promise<void> {
  await page.goto(projectUrl(path));
}
/**
 * Click the sidebar row for `projectId`. With per-worker projects, `.project-item` +
 * `.first()` picks somebody else's project.
 */
export async function selectProject(page: Page, projectId: string): Promise<void> {
  // Suffix match, not substring: `project=kanban-test-repo` is a prefix of
  // `project=kanban-test-repo-2`, and a substring match there is a strict-mode violation.
  await page.locator(`a.project-item[href$="project=${projectId}"]`).click();
}
/**
 * The directory a project points at — what its file browser, Review-Changes panel and
 * project-level git commands read. Not a task worktree: use findWorktreeDir for task flows.
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
  timeout = 15000,
): Promise<void> {
  await expect(async () => {
    const task = await getTaskData(page, projectId, taskId);
    expect(predicate(task)).toBe(true);
  }).toPass({ timeout });
}
/**
 * Idle: the send button is up. Budget for a whole LLM turn, not for a DOM wait — the default
 * 10s expect.timeout is shorter than one queued turn.
 */
export async function waitForSendButton(page: Page, timeout = 45_000): Promise<void> {
  await expect(page.locator('.btn-send')).toBeVisible({ timeout });
}
/**
 * Like waitForSendButton, but resolves false instead of throwing. It must actually wait:
 * callers test.skip() on false, so a check that returns immediately skips runnable tests.
 */
export async function waitForSendButtonGraceful(page: Page, timeout = 45_000): Promise<boolean> {
  return page.locator('.btn-send').waitFor({ state: 'visible', timeout })
      .then(() => true)
      .catch(() => false);
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
  await expect(userMessages.last(), 'the sent message is the one we typed').toContainText(message);
}

// --- Task lifecycle helpers ---

/**
 * Click "Start Task" if the task is in new state.
 * Private helper used by waitForChatForm.
 */
async function clickStartTaskIfNew(page: Page): Promise<void> {
  const startButton = page.locator('.btn-start-task');
  if (!(await startButton.isVisible().catch(() => false))) {
    return; // Button not visible, task is not new
  }
  await startButton.click().catch(() => {});
}

/**
 * Move task to in_progress via API (bypasses executor, for UI-only tests).
 * This makes the chat form appear without needing the executor/LLM.
 * Returns true if the task is now in_progress.
 */
export async function ensureTaskStarted(page: Page, projectId: string, taskId: string): Promise<boolean> {
  // Check if task is already not in backlog
  const taskData = await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.ok ? res.json() : null;
  }, { pid: projectId, tid: taskId });

  if (taskData?.status !== 'backlog') {
    return true; // Already started
  }

  // Move to in_progress via API (bypasses executor)
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
    method: 'POST',
    body: { status: 'in_progress' },
  });
  if (!result.ok) return false;

  // Verify the status change took effect
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
  // Wait for either the chat form or the restart button
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

/**
 * Ensure chat form is ready for input (send button visible, isLoading=false).
 * Waits for the LLM to finish any pending response before resolving.
 */
export async function ensureChatForm(page: Page, requireSendBtn = true): Promise<void> {
  await waitForChatForm(page);
  if (requireSendBtn) {
    // Turn budget, same as waitForSendButton: the executor may still be answering.
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 45_000 });
  }
}

// --- Navigation helpers ---

/**
 * Navigate to a task detail page using window.location (triggers full reload for SPA).
 * page.goto() doesn't re-trigger onMount for SPAs, so we use window.location.href instead.
 */
export async function navigateToTask(page: Page, taskId: string, projectId?: string): Promise<void> {
  if (!taskId) {
    throw new Error(`navigateToTask called with undefined/empty taskId`);
  }
  // Without an explicit project the app resolves `?task=` against its own default
  // project, which is not this file's project once a spec is isolated — so pass ours.
  const pid = projectId || ownProject?.id;
  const path = pid ? `/?project=${pid}&task=${taskId}` : `/?task=${taskId}`;
  await page.evaluate((u) => { window.location.href = u; }, path);
  await expect(page.locator('.task-detail-page')).toBeVisible();
}

// --- Task creation helpers ---

/**
 * Create a task via the API and return its ID.
 * Uses csrfFetch for CSRF-aware API calls. Direct API creation is faster and more reliable than UI modal.
 */
export async function createTaskViaApi(page: Page, projectId: string, prompt: string): Promise<string> {
  // Create task via API (CSRF-aware)
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
  // Wait for the board to pick up the new task (WebSocket push or next poll)
  await expect(async () => {
    const tasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return await res.json();
    }, projectId);
    expect(tasks.tasks.some((t: any) => t.id === taskId)).toBe(true);
  }).toPass({ timeout: 5000 });
  return taskId;
}
/**
 * First worktree directory of `projectId`, polling until it exists (a worktree appears only
 * once a task starts). Keyed by project: the worktrees dir holds every worker's projects.
 */
export async function findWorktreeDir(projectId: string, timeoutMs = 10000): Promise<string | null> {
  const projDir = `/tmp/kanban-test-worktrees/${projectId}`;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      const first = readdirSync(projDir).find((t) => existsSync(join(projDir, t, '.git')));
      if (first) return join(projDir, first);
    } catch { /* the project has no worktrees directory yet */ }
    if (Date.now() > deadline) return null;
    await new Promise((r) => setTimeout(r, 200));
  }
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
  // Check for a proper git worktree (has .git file pointing to the main repo)
  return existsSync(path) && existsSync(`${path}/.git`);
}

export function writeFileInWorktree(projectId: string, taskId: string, filePath: string, content: string): void {
  const worktreePath = getWorktreePath(projectId, taskId);
  const target = join(worktreePath, filePath);
  // The file's own directories, not just the worktree root: a fixture that edits a nested
  // path (`src/lib/utils/format.ts`) would otherwise fail with ENOENT on the write.
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, content);
  // Stage the file so git detects it
  try {
    execSync(`cd ${worktreePath} && git add ${filePath}`);
  } catch {
    // git add might fail if worktree isn't fully initialized; untracked files are still detected
  }
}

// --- Extension helpers ---

/**
 * True when an enabled LSP extension declares `suffix`. The host binary is not the prerequisite a
 * language-server test has — without the installed extension there is no server to talk to.
 */
export async function hasInstalledLspExtension(page: Page, suffix: string): Promise<boolean> {
  // GET, not csrfFetch: that one always sends a JSON content-type with no body, which
  // `/api/extensions` (a GET-only route) rejects — the helper then read "no extensions"
  // and every LSP spec quietly skipped itself.
  const list = (await apiGetJson<any[]>(page, '/api/extensions')) || [];
  return list.some((e) => {
    if (e?.type !== 'lsp' || !e?.enabled) return false;
    const types = e?.manifest?.capabilities?.lsp?.file_types || [];
    return types.some((ft: any) => ft?.extension === suffix);
  });
}
