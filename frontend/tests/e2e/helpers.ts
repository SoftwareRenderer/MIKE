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

// Helper: ensure chat form is ready for input (handles new-task "Start Task" state)
// Waits for the send button to be visible, meaning isLoading=false and form is interactive.
export async function ensureChatForm(page: Page, timeout = 5000): Promise<void> {
  // Click "Start Task" button if visible (new task state).
  // isVisible() + click() may have a race, so we retry in a loop.
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
  // Wait for send button (only visible when isLoading=false)
  // This ensures the LLM has finished any pending response
  await expect(page.locator('.btn-send')).toBeVisible({ timeout });
}