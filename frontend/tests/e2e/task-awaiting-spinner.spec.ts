import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task Start via API', () => {
  test.setTimeout(30000);

  test('should change task status to in_progress when started via API', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `api-start-test-${Date.now()}`);

    // Check initial status (board column)
    const initialStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(initialStatus).toBe('backlog');

    // Start the task via API (simulates starting from the board)
    await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
      if (!res.ok) throw new Error(`Failed to start task: ${res.status}`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    // Navigate to the task detail
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify task status changed to in_progress via API
    const newStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return task.status;
    }, { pid: projectId, tid: taskId });
    expect(newStatus).toBe('in_progress');
  });
});
