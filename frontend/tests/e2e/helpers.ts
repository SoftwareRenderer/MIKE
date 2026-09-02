import { expect, Page, Locator } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync, readFileSync } from 'fs';
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

/** Get the first project ID from the API. */
export async function getProjectId(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/projects');
    const projects = await res.json();
    return projects[0]?.id || '';
  });
}

/** Get the last task ID from the board API. */
export async function getLastTaskId(page: Page, projectId: string): Promise<string> {
  return await page.evaluate(async ({ pid }) => {
    const res = await fetch(`/api/projects/${pid}/board`);
    const board = await res.json();
    return board.tasks[board.tasks.length - 1]?.id || '';
  }, { pid: projectId });
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

// --- Chat form readiness helpers ---

/**
 * Wait for the send button to be visible (chat form ready for input, isLoading=false).
 * Use this BEFORE interacting with the textarea to ensure the form is stable.
 */
export async function waitForSendButton(page: Page): Promise<void> {
  await expect(page.locator('.btn-send')).toBeVisible();
}

/**
 * Like waitForSendButton but returns false instead of throwing on timeout.
 * Use this with test.skip() for graceful handling of executor-dependent tests.
 */
export async function waitForSendButtonGraceful(page: Page): Promise<boolean> {
  return page.locator('.btn-send').isVisible().catch(() => false);
}

/**
 * Wait for the chat form to be interactive (send OR stop button visible).
 * Use this after starting a task when the executor may be calling the LLM.
 */
export async function waitForChatFormReady(page: Page): Promise<void> {
  await expect(page.locator('.chat-form')).toBeVisible();
  // Wait for either the send button (idle) or stop button (LLM responding)
  await expect(async () => {
    const hasSend = await page.locator('.btn-send').isVisible();
    const hasStop = await page.locator('.btn-stop').isVisible();
    expect(hasSend || hasStop).toBe(true);
  }).toPass();
}

// --- Message helpers ---

/**
 * Wait for a new user message to appear in the chat (after sending).
 * Compares count before/after to detect the new message.
 */
async function waitForNewUserMessage(page: Page): Promise<void> {
  const countBefore = await page.locator('.chat-message.user').count();
  await expect(async () => {
    const count = await page.locator('.chat-message.user').count();
    expect(count).toBeGreaterThan(countBefore);
  }).toPass();
}

/**
 * Send a message via the chat UI.
 * Waits for send button (form stable) → fills → presses Enter → waits for user message to appear.
 * Uses Playwright auto-wait (no arbitrary timeouts).
 */
export async function sendMessage(page: Page, message: string): Promise<void> {
  // Wait for the send button to be visible (form is stable, not loading).
  await waitForSendButton(page);
  // Fill and send — Playwright auto-waits for textarea to be actionable
  const chatTextarea = page.locator('textarea[placeholder*="message"]');
  await chatTextarea.fill(message);
  await chatTextarea.press('Enter');
  // Wait for the user message to appear in chat (confirms message was sent)
  await waitForNewUserMessage(page);
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
    await expect(page.locator('.btn-send')).toBeVisible();
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
  const path = projectId ? `/?project=${projectId}&task=${taskId}` : `/?task=${taskId}`;
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
 * Create a fresh task and open it via URL.
 * Uses the API to create the task (avoids race with LLM title generation replacing card text)
 * then navigates directly to the task detail page.
 * Returns the created task ID.
 */
export async function createAndOpenTask(page: Page, title?: string): Promise<string> {
  const taskPrompt = title || `task-${Date.now()}`;
  const projectId = await getProjectId(page);
  const taskId = await createTaskViaApi(page, projectId, taskPrompt);
  await navigateToTask(page, taskId);
  return taskId;
}

// --- Worktree helpers (test servers use MIKE_WORKTREES_DIR=/tmp/kanban-test-worktrees) ---

export function getWorktreePath(projectId: string, taskId: string): string {
  return `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
}

export function worktreeExists(projectId: string, taskId: string): boolean {
  const path = getWorktreePath(projectId, taskId);
  // Check for a proper git worktree (has .git file pointing to the main repo)
  return existsSync(path) && existsSync(`${path}/.git`);
}

export function writeFileInWorktree(projectId: string, taskId: string, filePath: string, content: string): void {
  const worktreePath = getWorktreePath(projectId, taskId);
  mkdirSync(worktreePath, { recursive: true });
  writeFileSync(`${worktreePath}/${filePath}`, content);
  // Stage the file so git detects it
  try {
    execSync(`cd ${worktreePath} && git add ${filePath}`);
  } catch {
    // git add might fail if worktree isn't fully initialized; untracked files are still detected
  }
}

// --- Agent extension helpers (isolated server) ---
//
// Agent providers are registry-derived: they exist only while the corresponding
// agent extension is installed AND enabled. These helpers install/uninstall the
// local `pi` agent extension (already materialized in the repo's extensions/
// directory, which the test servers scan from their CWD).

/**
 * Install (if needed) and enable the local `pi` agent extension on the server
 * backing `page`. Returns the extension ID. Idempotent: re-enables if already
 * installed (e.g. a rerun after an interrupted prior run).
 */
export async function installPiExtension(page: Page): Promise<string> {
  const existingId = await getPiExtensionId(page);
  if (existingId) {
    await setExtensionEnabledById(page, existingId, true);
    return existingId;
  }

  const { entry, csrf } = await page.evaluate(async () => {
    const sessionRes = await fetch('/api/auth/session');
    const session = await sessionRes.json();
    const localRes = await fetch('/api/extensions/local');
    const local = await localRes.json();
    return { entry: local.find((e: any) => e.name === 'pi') || null, csrf: session.csrf_token || '' };
  });
  if (!entry) {
    throw new Error('pi extension not found in local extensions list (extensions/ dir not materialized?)');
  }

  // install-local streams SSE; the fetch resolves when the install completes.
  const install = await page.evaluate(async ({ author, csrfToken }) => {
    const r = await fetch('/api/extensions/install-local', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      credentials: 'include',
      body: JSON.stringify({ author, name: 'pi' }),
    });
    const text = await r.text();
    return { status: r.status, text };
  }, { author: entry.author, csrfToken: csrf });
  if (install.status !== 200 && install.status !== 201) {
    throw new Error(`install-local failed (${install.status}): ${install.text.slice(0, 300)}`);
  }

  const id = await getPiExtensionId(page);
  if (!id) throw new Error('pi extension not found after install');
  await setExtensionEnabledById(page, id, true);
  return id;
}

/** Uninstall the local `pi` agent extension if present (best-effort). */
export async function deletePiExtension(page: Page): Promise<void> {
  const id = await getPiExtensionId(page);
  if (!id) return;
  const res = await csrfFetch(page, `/api/extensions/${id}`, { method: 'DELETE' });
  if (!res.ok && res.status !== 404) {
    throw new Error(`delete extension failed (${res.status})`);
  }
}

/** The installed `pi` extension's ID, or '' when not installed. */
export async function getPiExtensionId(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/extensions');
    if (!res.ok) return '';
    const exts = await res.json();
    return exts.find((e: any) => e.name === 'pi')?.id || '';
  });
}

async function setExtensionEnabledById(page: Page, id: string, enabled: boolean): Promise<void> {
  const res = await csrfFetch(page, `/api/extensions/${id}/enabled`, { method: 'PATCH', body: { enabled } });
  if (!res.ok) throw new Error(`set extension enabled failed (${res.status})`);
}

/**
 * True when an enabled LSP extension declares `suffix`. The host binary is not the prerequisite a
 * language-server test has — without the installed extension there is no server to talk to.
 */
export async function hasInstalledLspExtension(page: Page, suffix: string): Promise<boolean> {
  const body = await csrfFetch(page, '/api/extensions');
  const list: any[] = Array.isArray(body) ? body : (body?.extensions || []);
  return list.some((e) => {
    if (e?.type !== 'lsp' || !e?.enabled) return false;
    const types = e?.manifest?.capabilities?.lsp?.file_types || [];
    return types.some((ft: any) => ft?.extension === suffix);
  });
}
