import { test as base, expect, Page } from '@playwright/test';

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
  await expect(page.locator('.modal')).not.toBeVisible();
  await page.waitForSelector('.card', { timeout: 5000 });

  // Open the newly created task by finding the card with matching title
  // (using .last() is unreliable when many tasks share the same column_order)
  const taskCard = page.locator('.card').filter({ hasText: taskTitle });
  await expect(taskCard).toBeVisible({ timeout: 5000 });
  await taskCard.click();
  await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

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
export async function createTaskViaApi(page: Page, projectId: string, prompt: string): Promise<string> {
  return await page.evaluate(async ({ pid, prompt }) => {
    const res = await fetch(`/api/projects/${pid}/tasks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ prompt }),
    });
    const task = await res.json();
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
