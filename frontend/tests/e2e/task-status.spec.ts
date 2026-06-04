import { test, expect } from '@playwright/test';
import { getProjectId, getLastTaskId, getTaskData } from './helpers';

test.describe.serial('Task Status States', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should set status stopped when user stops a running task', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `stop test ${Date.now()}`, prompt: 'stop button test' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Start the task via API
    const startRes = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
      return { status: res.status, data: await res.json() };
    }, { pid: projectId, tid: taskId });
    expect(startRes.status).toBe(200);
    expect(startRes.data.status_state).toBe('running');

    // Wait a moment for the task to begin processing
    await page.waitForTimeout(1000);

    // Stop the task via API
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/stop`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for the executor goroutine to process ctx.Done()
    await page.waitForTimeout(2000);

    // Check the task status via API
    const taskData = await getTaskData(page, projectId, taskId);

    // Should be "stopped", NOT "failed"
    expect(taskData.status_state).toBe('stopped');
    expect(taskData.status_state).not.toBe('failed');
  });

  test('should set status failed only for unrecoverable tool call failures', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `fail test ${Date.now()}`, prompt: 'read a file that does not exist' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Set max_consecutive_failed_turns to 1 so it fails after first tool failure
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ max_consecutive_failed_turns: 1 }),
      });
    }, { pid: projectId, tid: taskId });

    // Start the task via API (with retry for transient conflicts under parallel workers)
    let startRes: { status: number; data: any };
    for (let attempt = 0; attempt < 3; attempt++) {
      if (attempt > 0) await page.waitForTimeout(500);
      startRes = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
        return { status: res.status, data: await res.json() };
      }, { pid: projectId, tid: taskId });
      if (startRes.status === 200) break;
    }
    expect(startRes.status).toBe(200);
    expect(startRes.data.status_state).toBe('running');

    // Poll for the task to reach failed status (tool call fails -> max failures reached)
    let taskData: Record<string, unknown>;
    let iterations = 0;
    const maxIterations = 30; // 30 * 500ms = 15s max
    do {
      await page.waitForTimeout(500);
      taskData = await getTaskData(page, projectId, taskId);
      iterations++;
    } while (taskData.status_state === 'running' && iterations < maxIterations);

    // Should be "failed" because of unrecoverable tool call failures
    expect(taskData.status_state).toBe('failed');
  });
});
