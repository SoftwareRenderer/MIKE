import { test, expect } from '@playwright/test';
import { getProjectId, getTaskData, createTaskViaApi } from './helpers';

test.describe.serial('Task Auto-Start on Move', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should auto-start a task when moved from backlog to in_progress', async ({ page }) => {
    // Create a task via API with mock LLM config
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: `auto-start test ${Date.now()}`,
          prompt: 'hello',
          llama_server_url: 'http://localhost:9999/v1',
          model_name: 'mock-model',
        }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Verify task is in backlog
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status).toBe('backlog');
    expect(taskData.status_state).not.toBe('running');
    expect(taskData.status_state).not.toBe('completed');

    // Move task to in_progress via the move endpoint (same as drag-and-drop)
    const moveRes = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
      return { status: res.status, data: await res.json() };
    }, { pid: projectId, tid: taskId });
    expect(moveRes.status).toBe(200);

    // Task should now be in in_progress column
    expect(moveRes.data.status).toBe('in_progress');

    // Wait for executor to process (status_state transitions: running → idle after mock LLM responds)
    // Mock LLM is fast, so we check for non-empty status_state (proves executor ran)
    await expect(async () => {
      taskData = await getTaskData(page, projectId, taskId);
      expect(taskData.status_state).not.toBe('');
      expect(taskData.status_state).not.toBe('failed');
    }).toPass({ timeout: 10000 });

    // Verify task messages were created (proves LLM was actually called)
    const messages = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(messages.length).toBeGreaterThan(0);

    // Clean up: stop the task (best-effort, may already be idle)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/stop`, { method: 'POST' });
    }, { pid: projectId, tid: taskId }).catch(() => {});
    await page.waitForTimeout(500);
  });

  test('should set status_state failed if start fails on move', async ({ page }) => {
    // Create a task WITHOUT llama_server_url so StartTask fails synchronously
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          title: `fail-start test ${Date.now()}`,
          prompt: 'hello',
          // No llama_server_url — StartTask returns "llama server URL not configured"
        }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Verify task is in backlog and not running
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status).toBe('backlog');
    expect(taskData.status_state).not.toBe('running');

    // Move task to in_progress — start will fail because LLM is unreachable
    const moveRes = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
      return { status: res.status, data: await res.json() };
    }, { pid: projectId, tid: taskId });
    expect(moveRes.status).toBe(200);

    // Task should still move to in_progress column
    expect(moveRes.data.status).toBe('in_progress');

    // But status_state should be "failed" because start failed
    taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('failed');
    expect(taskData.output).toBeTruthy();
  });
});
