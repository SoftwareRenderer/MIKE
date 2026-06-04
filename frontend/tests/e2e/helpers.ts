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