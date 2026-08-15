import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

/**
 * Chat input initial focus.
 *
 * After opening a task, the chat input must have initial focus — but only if it
 * is actually rendered. Backlog tasks show the StartPanel instead, and the
 * "restart" state shows a restart button instead of the form, so in those
 * states nothing should be focused.
 */
test.describe.configure({ mode: 'serial' });

test.describe('Chat input initial focus', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  /** Create a task via API and move it to the given status (no LLM needed). */
  async function createTaskWithStatus(page: Page, status: string, tag: string): Promise<string> {
    const taskId = await createTaskViaApi(page, projectId, `focus-${tag}-${Date.now()}`);
    const res = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status },
    });
    expect(res.ok).toBe(true);
    return taskId;
  }

  test('focuses the chat input when a task with an input is opened', async ({ page }) => {
    // `review` renders ReviewBar + ChatForm and does not trigger the executor
    // (only in_progress does), so this stays deterministic with no LLM.
    const taskId = await createTaskWithStatus(page, 'review', 'open');
    await navigateToTask(page, taskId, projectId);
    const chatTextarea = page.locator('.chat-form textarea');
    await expect(chatTextarea).toBeVisible({ timeout: 10000 });
    await expect(chatTextarea).toBeFocused({ timeout: 10000 });
  });

  test('renders no chat input for backlog tasks (nothing to focus)', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `focus-backlog-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    // Backlog renders the start panel — wait for it so the assertion is not
    // racing a mid-render state.
    await expect(page.locator('.chat-start')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-form textarea')).toHaveCount(0);
  });

  test('focus moves to the new task input when switching tasks in-app', async ({ page }) => {
    const taskA = await createTaskWithStatus(page, 'review', 'switch-a');
    const taskB = await createTaskWithStatus(page, 'review', 'switch-b');
    await navigateToTask(page, taskA, projectId);
    const inputA = page.locator('.chat-form textarea');
    await expect(inputA).toBeVisible({ timeout: 10000 });
    await expect(inputA).toBeFocused({ timeout: 10000 });

    // Switch in-app via the Board Overview card (TaskChat remounts on task change)
    const cardB = page.locator(`.column-context .card[data-task-id="${taskB}"]`).first();
    await cardB.click();
    const inputB = page.locator('.chat-form textarea');
    await expect(inputB).toBeVisible({ timeout: 10000 });
    await expect(inputB).toBeFocused({ timeout: 10000 });
  });
});
