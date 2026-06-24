import { expect, Page, Locator } from '@playwright/test';
import http from 'node:http';

const TEST_PASSWORD = 'test-password-1234';

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
 * Get CSRF token from page context. Tries window.__csrfToken first,
 * then falls back to fetching /api/auth/session.
 */
async function getCsrfToken(page: Page): Promise<string> {
  let token = await page.evaluate(() => (window as any).__csrfToken || '');
  if (!token) {
    token = await page.evaluate(async () => {
      try {
        const res = await fetch('/api/auth/session');
        const data = await res.json();
        return data.csrf_token || '';
      } catch {
        return '';
      }
    });
  }
  return token;
}

/**
 * CSRF-aware fetch wrapper for use in page.evaluate.
 * Use this instead of raw fetch for POST/PUT/DELETE requests.
 * Returns { ok, status, json, text } from the response.
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
  // Ensure page has a valid origin
  await getOrigin(page);
  // Get CSRF token: first from window.__csrfToken (set by login fixture),
  // then from session endpoint as fallback
  let csrfToken = await page.evaluate(() => (window as any).__csrfToken || '');
  if (!csrfToken) {
    csrfToken = await page.evaluate(async () => {
      const res = await fetch('/api/auth/session');
      const data = await res.json();
      return data.csrf_token || '';
    });
  }
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
  // Ensure page has a valid origin
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

// --- Condition-based wait helpers (replaces arbitrary sleeps) ---

/** Wait for a locator's attribute to change from its current value. */
export async function waitForAttributeChange(locator: Locator, attr: string, timeout = 5000): Promise<void> {
  const initialValue = await locator.getAttribute(attr);
  await expect(async () => {
    const currentValue = await locator.getAttribute(attr);
    expect(currentValue).not.toBe(initialValue);
  }).toPass({ timeout });
}

/** Wait for a locator's text content to change from its current value. */
export async function waitForTextChange(locator: Locator, timeout = 5000): Promise<void> {
  const initialText = await locator.textContent();
  await expect(async () => {
    const currentText = await locator.textContent();
    expect(currentText).not.toBe(initialText);
  }).toPass({ timeout });
}

/** Wait for a locator to have a specific count (replaces sleep after add/remove). */
export async function waitForCount(locator: Locator, expectedCount: number, timeout = 5000): Promise<void> {
  await expect(locator).toHaveCount(expectedCount, { timeout });
}

/** Wait for an element to disappear from the DOM. */
export async function waitForHidden(locator: Locator, timeout = 5000): Promise<void> {
  await expect(locator).toBeHidden({ timeout });
}

/** Wait for bounding box width to change (for resize tests). */
export async function waitForWidthChange(locator: Locator, timeout = 5000): Promise<void> {
  const initialBox = await locator.boundingBox();
  const initialWidth = initialBox?.width || 0;
  await expect(async () => {
    const box = await locator.boundingBox();
    expect(box?.width).not.toBe(initialWidth);
  }).toPass({ timeout });
}

// Helper: get the first project ID from the API.
export async function getProjectId(page: Page): Promise<string> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/projects');
    const projects = await res.json();
    return projects[0]?.id || '';
  });
}

// Helper: get the last task ID from the board API
export async function getLastTaskId(page: Page, projectId: string): Promise<string> {
  return await page.evaluate(async ({ pid }) => {
    const res = await fetch(`/api/projects/${pid}/board`);
    const board = await res.json();
    return board.tasks[board.tasks.length - 1]?.id || '';
  }, { pid: projectId });
}

// Helper: get task data from the API
export async function getTaskData(page: Page, projectId: string, taskId: string): Promise<Record<string, unknown>> {
  return await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
    return res.json();
  }, { pid: projectId, tid: taskId });
}

// Helper: wait for WebSocket to connect by checking chat form is interactive
export async function waitForWebSocket(page: Page): Promise<void> {
  await expect(page.locator('.chat-form')).toBeVisible();
}

// Helper: wait for the send button to be visible (chat form ready for input, isLoading=false).
// Use this BEFORE interacting with the textarea to ensure the form is stable.
export async function waitForSendButton(page: Page): Promise<void> {
  await expect(page.locator('.btn-send')).toBeVisible();
}

// Helper: like waitForSendButton but returns false instead of throwing on timeout.
// Use this with test.skip() for graceful handling of executor-dependent tests.
export async function waitForSendButtonGraceful(page: Page): Promise<boolean> {
  return page.locator('.btn-send').isVisible().catch(() => false);
}

// Helper: wait for the chat form to be interactive (send OR stop button visible).
// Use this after starting a task when the executor may be calling the LLM.
export async function waitForChatFormReady(page: Page): Promise<void> {
  await expect(page.locator('.chat-form')).toBeVisible();
  // Wait for either the send button (idle) or stop button (LLM responding)
  await expect(async () => {
    const hasSend = await page.locator('.btn-send').isVisible();
    const hasStop = await page.locator('.btn-stop').isVisible();
    expect(hasSend || hasStop).toBe(true);
  }).toPass();
}

// Helper: wait for a new user message to appear in the chat (after sending).
// Compares count before/after to detect the new message.
export async function waitForNewUserMessage(page: Page): Promise<void> {
  const countBefore = await page.locator('.chat-message.user').count();
  await expect(async () => {
    const count = await page.locator('.chat-message.user').count();
    expect(count).toBeGreaterThan(countBefore);
  }).toPass();
}

// Helper: wait for chat message to appear
export async function waitForChatMessage(page: Page): Promise<void> {
  await expect(page.locator('.chat-message')).toBeVisible();
}

// Helper: click "Start Task" if the task is in new state
async function clickStartTaskIfNew(page: Page): Promise<void> {
  const startButton = page.locator('.btn-start-task');
  if (!(await startButton.isVisible().catch(() => false))) {
    return; // Button not visible, task is not new
  }
  await startButton.click().catch(() => {});
}

// Helper: move task to in_progress via API (bypasses executor, for UI-only tests)
// This makes the chat form appear without needing the executor/LLM.
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

// Helper: wait for the chat form to appear. Handles the case where the "Restart Task"
// button is shown (executor hasn't responded to initial prompt yet).
// Returns true if the chat form is visible, false otherwise.
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

// Helper: ensure chat form is ready for input (send button visible, isLoading=false)
// Waits for the LLM to finish any pending response before resolving.
export async function ensureChatForm(page: Page, requireSendBtn = true): Promise<void> {
  await waitForChatForm(page);
  if (requireSendBtn) {
    await expect(page.locator('.btn-send')).toBeVisible();
  }
}

// Helper: navigate to a task detail page using window.location (triggers full reload for SPA).
// page.goto() doesn't re-trigger onMount for SPAs, so we use window.location.href instead.
export async function navigateToTask(page: Page, taskId: string, projectId?: string): Promise<void> {
  if (!taskId) {
    throw new Error(`navigateToTask called with undefined/empty taskId`);
  }
  const path = projectId ? `/?project=${projectId}&task=${taskId}` : `/?task=${taskId}`;
  await page.evaluate((u) => { window.location.href = u; }, path);
  await expect(page.locator('.task-detail-page')).toBeVisible();
}

// Helper: create a fresh task and open it via URL
// Uses the API to create the task (avoids race with LLM title generation replacing card text)
// then navigates directly to the task detail page.
export async function createAndOpenTask(page: Page, title?: string): Promise<string> {
  const taskPrompt = title || `task-${Date.now()}`;
  const projectId = await getProjectId(page);
  const taskId = await createTaskViaApi(page, projectId, taskPrompt);
  await navigateToTask(page, taskId);
  return taskId;
}

// Helper: send a message via the chat UI.
// Waits for send button (form stable) → fills → presses Enter → waits for user message to appear.
// Uses Playwright auto-wait (no arbitrary timeouts).
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

// Helper: send a message and wait for the LLM response to complete.
// Returns after the send button reappears (isLoading=false).
export async function sendMessageAndWait(page: Page, message: string): Promise<void> {
  await sendMessage(page, message);
  // Wait for send button to reappear (LLM complete)
  await expect(page.locator('.btn-send')).toBeVisible();
}

// Helper: create a task via the API and return its ID.
// Uses csrfFetch for CSRF-aware API calls. Direct API creation is faster and more reliable than UI modal.
export async function createTaskViaApi(page: Page, projectId: string, prompt: string): Promise<string> {
  // Get existing task IDs before creating
  const existingIds = await page.evaluate(async (pid) => {
    const res = await fetch(`/api/projects/${pid}/board`);
    const board = await res.json();
    return board.tasks.map((t: any) => t.id);
  }, projectId);
  // Create task via API (CSRF-aware)
  const config = await apiGetConfig(page);
  const defaultProvider = config?.providers?.find((p: any) => p.name === config?.default_provider);
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: {
      prompt,
      llama_server_url: defaultProvider?.url || '',
      model_name: defaultProvider?.default_model || '',
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

// Helper: send a message via HTTP POST to trigger lazy-start.
// This bypasses the "Start Task" button and avoids the first executor.
export async function sendViaWebSocket(page: Page, projectId: string, taskId: string, message: string): Promise<void> {
  return await page.evaluate(async ({ pid, tid, msg }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content: msg }),
    });
    if (!res.ok) {
      throw new Error(`Send failed: ${res.status}`);
    }
    await res.json();
  }, { pid: projectId, tid: taskId, msg: message });
}
