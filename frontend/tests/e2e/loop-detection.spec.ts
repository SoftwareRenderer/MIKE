import { test, expect } from '@playwright/test';
import { createAndOpenTask, ensureChatForm, getProjectId, getTaskData } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Loop Detection', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should detect tool-call loop and recover after hint injection', async ({ page }) => {
    // Create a task with a keyword that triggers the mock to repeat the same tool call.
    // The backend should detect the loop (same target read twice without writes),
    // inject a hint, and the mock should then return task_complete.
    const taskId = await createAndOpenTask(page, `loop-test-tool: read a file ${Date.now()}`);
    await ensureChatForm(page);

    // Send a message to trigger the executor (lazy-start)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('loop-test-tool: please read the file');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for the task to reach a terminal state.
    // Flow: tool call (head file) → tool call again (same target, loop!) → hint injected → task_complete.
    // The task should reach "awaiting_review" or "done" within a reasonable time.
    const projectId = await getProjectId(page);
    await expect(async () => {
      const task = await getTaskData(page, projectId, taskId);
      const state = task.status_state as string;
      expect(['completed', 'awaiting_review', 'failed', 'stopped']).toContain(state);
    }).toPass({ timeout: 60000 });

    // Verify the task did NOT fail with a loop error (it should have recovered).
    const finalTask = await getTaskData(page, projectId, taskId);
    expect(finalTask.output).not.toContain('loop');
  });
});
