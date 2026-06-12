import { test as base, expect, Page, Locator } from '@playwright/test';
import http from 'node:http';

// Custom fixture that provides helper methods
export const test = base.extend<{
  waitForAppLoaded: () => Promise<void>;
  openFirstTask: () => Promise<void>;
  getTaskIdFromApi: () => Promise<string | null>;
}>({
  waitForAppLoaded: async ({ page }, use) => {
    use(async () => {
      await page.waitForSelector('.project-item, .card', { timeout: 10000 });
    });
  },

  openFirstTask: async ({ page }, use) => {
    use(async () => {
      await page.waitForSelector('.card', { timeout: 10000 });
      await page.click('.card');
      await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    });
  },

  getTaskIdFromApi: async ({ page }, use) => {
    use(async () => {
      return await page.evaluate(async () => {
        const res = await fetch('/api/projects');
        const projects = await res.json();
        if (projects.length === 0) return null;
        const boardRes = await fetch(`/api/projects/${projects[0].id}/board`);
        const board = await boardRes.json();
        return board.tasks && board.tasks.length > 0 ? board.tasks[0].id : null;
      });
    });
  },
});

export { expect } from '@playwright/test';

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

/** PUT /api/config with the given body and wait for it to propagate. */
export async function apiPutConfig(page: Page, body: Record<string, any>): Promise<any> {
  const origin = await getOrigin(page);
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const req = http.request(`${origin}/api/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) },
    }, (res) => {
      let responseBody = '';
      res.on('data', chunk => responseBody += chunk);
      res.on('end', () => {
        try { resolve(JSON.parse(responseBody)); } catch { resolve(null); }
      });
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

/** GET /api/config and return parsed JSON. */
export async function apiGetConfig(page: Page): Promise<any> {
  const origin = await getOrigin(page);
  return new Promise((resolve, reject) => {
    http.get(`${origin}/api/config`, (res) => {
      let data = '';
      res.on('data', chunk => data += chunk);
      res.on('end', () => {
        try { resolve(JSON.parse(data)); } catch { resolve(null); }
      });
    }).on('error', reject);
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

// Helper: get the first project ID from the API
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
export async function waitForWebSocket(page: Page, timeout = 5000): Promise<void> {
  await expect(page.locator('.chat-form')).toBeVisible({ timeout });
}

// Helper: wait for textarea to be cleared after sending message
export async function waitForMessageSent(page: Page, timeout = 5000): Promise<void> {
  await expect(page.locator('textarea[placeholder*="message"], textarea[placeholder*="Send"]')).toHaveValue('', { timeout });
}

// Helper: wait for chat message to appear
export async function waitForChatMessage(page: Page, timeout = 10000): Promise<void> {
  await expect(page.locator('.chat-message')).toBeVisible({ timeout });
}

// Helper: click "Start Task" if the task is in new state
async function clickStartTaskIfNew(page: Page): Promise<void> {
  for (let attempt = 0; attempt < 3; attempt++) {
    const startButton = page.locator('.btn-start-task');
    if (!(await startButton.isVisible().catch(() => false))) {
      break; // Button not visible, task is not new
    }
    try {
      await startButton.click({ timeout: 5000 });
      break; // Click succeeded
    } catch {
      // Button may have been detached - retry
    }
  }
}

// Helper: wait for the chat form to be present (send OR stop button visible)
// Use this when you need the form to exist but don't care if LLM is still responding.
export async function waitForChatForm(page: Page, timeout = 5000): Promise<void> {
  await clickStartTaskIfNew(page);
  await expect(page.locator('.chat-form')).toBeVisible({ timeout });
}

// Helper: ensure chat form is ready for input (send button visible, isLoading=false)
// Waits for the LLM to finish any pending response before resolving.
export async function ensureChatForm(page: Page, timeout = 10000): Promise<void> {
  await clickStartTaskIfNew(page);
  // Wait for send button (only visible when isLoading=false)
  // This ensures the LLM has finished any pending response
  await expect(page.locator('.btn-send')).toBeVisible({ timeout });
}

// Helper: create a fresh task via UI and open it
// Use this instead of clicking an existing card to avoid shared-state issues across parallel workers.
export async function createAndOpenTask(page: Page, title?: string): Promise<string> {
  const taskTitle = title || `task-${Date.now()}`;
  await page.click('.add-task-btn');
  await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
  await page.fill('textarea', taskTitle);
  await page.click('button[type="submit"]');
  // Wait for modal to close (removed from DOM) — use a longer timeout for heavy load
  await expect(page.locator('.modal')).toBeHidden({ timeout: 10000 });
  await page.waitForSelector('.card', { timeout: 5000 });

  // Open the newly created task by finding the card with matching title
  // (using .last() is unreliable when many tasks share the same column_order)
  const taskCard = page.locator('.card').filter({ hasText: taskTitle });
  await expect(taskCard).toBeVisible({ timeout: 10000 });
  await taskCard.click();
  await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

  return taskTitle;
}

// Helper: send a message and wait for the LLM response to complete
// Returns after the send button reappears (isLoading=false).
export async function sendMessageAndWait(page: Page, message: string, timeout = 15000): Promise<void> {
  const chatTextarea = page.locator('textarea[placeholder*="message"]');
  await chatTextarea.fill(message);
  await chatTextarea.press('Enter');
  // Wait for textarea to be cleared (message sent)
  await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
  // Wait for send button to reappear (LLM complete)
  await expect(page.locator('.btn-send')).toBeVisible({ timeout });
}

// Helper: create a task via API and return its ID
// This avoids the UI race conditions and is faster for tests that don't need UI interaction.
// Automatically includes the default provider's llama_server_url and model_name
// so the executor can start (tasks without LLM config won't have a running executor).
export async function createTaskViaApi(page: Page, projectId: string, prompt: string): Promise<string> {
  return await page.evaluate(async ({ pid, prompt }) => {
    // Fetch default provider config to include in task creation
    const [configRes] = await Promise.all([
      fetch('/api/config'),
    ]);
    const config = await configRes.json();
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

    const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        prompt,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      }),
    });
    const task = await taskRes.json();
    return task.id;
  }, { pid: projectId, prompt });
}

// Helper: send a message via WebSocket to trigger lazy-start
// This bypasses the "Start Task" button and avoids the first executor.
export async function sendViaWebSocket(page: Page, projectId: string, taskId: string, message: string): Promise<void> {
  return await page.evaluate(async ({ pid, tid, msg }) => {
    const ws = new WebSocket(`${window.location.origin}/api/projects/${pid}/tasks/${tid}/stream`);
    return new Promise<void>((resolve, reject) => {
      ws.onopen = () => {
        ws.send(JSON.stringify({ type: 'user_message', data: { content: msg } }));
      };
      ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
        if (data.type === 'user_message') {
          // Server echoed the message back — lazy-start should be triggered
          ws.close();
          resolve();
        }
      };
      ws.onerror = () => {
        ws.close();
        reject(new Error('WebSocket error'));
      };
      setTimeout(() => { ws.close(); reject(new Error('WebSocket timeout')); }, 10000);
    });
  }, { pid: projectId, tid: taskId, msg: message });
}
