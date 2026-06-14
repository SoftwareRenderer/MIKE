import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Awaiting LLM spinner on externally-started task', () => {
  test('should show awaiting spinner when opening a task that was started from the board', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `slow-start awaiting-spinner-${Date.now()}`);

    // Start the task via API (simulates starting from the board)
    await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
      if (!res.ok) throw new Error(`Failed to start task: ${res.status}`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    // Navigate to the task detail immediately (before mock LLM responds)
    // Mock LLM has a 50ms warm-up delay, giving us a window to load the page
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The "Awaiting LLM provider..." spinner should be visible
    // while we wait for the initial LLM response
    await expect(page.locator('.awaiting-llm')).toBeVisible({ timeout: 5000 });
  });
});
