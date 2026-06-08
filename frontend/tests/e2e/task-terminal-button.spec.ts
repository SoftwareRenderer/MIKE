import { test, expect } from '@playwright/test';
import { createAndOpenTask, waitForChatForm, getProjectId, getTaskData } from './helpers';

test.describe.serial('Terminal Button Enable on Task Start', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should disable terminal button before task starts, enable after worktree is created', async ({ page }) => {
    // Create and open a new task
    const taskTitle = `terminal-test-${Date.now()}`;
    await createAndOpenTask(page, taskTitle);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Terminal button should be disabled (no worktree yet for idle task)
    const terminalBtn = page.locator('button[aria-label="Open terminal"]');
    await expect(terminalBtn).toBeVisible({ timeout: 5000 });
    await expect(terminalBtn).toBeDisabled();

    // Get project and task IDs for API polling
    const projectId = await getProjectId(page);

    // Wait for chat form to be ready (handles "Start Task" button if in new state)
    await waitForChatForm(page, 10000);

    // Trigger lazy-start by sending a message via the chat
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('hello');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Poll the API to confirm the task started (worktree created) instead of
    // waiting for the UI to react (which depends on board broadcast timing)
    const taskId = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      // Find the task we just created (last one with matching title pattern)
      const task = board.tasks.reverse().find((t: any) => t.title?.includes('terminal-test'));
      return task?.id || '';
    }, { pid: projectId });

    // Wait for task status_state to become 'running' (implies worktree created)
    await page.waitForFunction(
      async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        const task = await res.json();
        return task.status_state === 'running';
      },
      { pid: projectId, tid: taskId },
      { timeout: 15000 }
    );

    // Now wait for the board broadcast to refresh the UI and enable the terminal button
    await expect(terminalBtn).toBeEnabled({ timeout: 10000 });
  });
});
