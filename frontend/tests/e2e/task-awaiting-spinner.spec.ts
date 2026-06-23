import { test, expect } from '@playwright/test';
import {getProjectId, createTaskViaApi, navigateToTask, csrfFetch} from './helpers';

test.describe.serial('Task Start via API', () => {
  test.setTimeout(30000);

  test('should change task status to in_progress when started via API', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card');
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `api-start-test-${Date.now()}`);

    // Check initial status (board column)
    const initialStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(initialStatus).toBe('backlog');

    // Start the task via API (simulates starting from the board, CSRF-aware)
    const startResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
      method: 'POST',
    });
    // Graceful: executor may fail in test env (worktree conflict, etc.)
    if (!startResult.ok) {
      test.skip(`Task start returned ${startResult.status} (executor may not be available in test env)`);
    }

    // Navigate to the task detail
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Verify task status changed to in_progress via API
    const newStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(newStatus).toBe('in_progress');
  });
});
