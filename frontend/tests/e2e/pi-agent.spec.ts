import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Coding Agent', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create and start a coding agent task without crashing', async ({ page }) => {
    test.setTimeout(30000);

    const projectId = await getProjectId(page);

    // Create a task with an agent URL
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `Agent test task ${Date.now()}`,
        llama_server_url: 'agent://pi',
        model_name: null,
      },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;
    expect(taskId).toBeTruthy();

    // Start the task by moving it to in_progress via API
    const moveResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${taskId}/move`,
      {
        method: 'POST',
        body: { status: 'in_progress' },
      }
    );
    expect(moveResult.ok).toBe(true);

    // Wait for the task to start processing (executor updates execution_state)
    // Poll until execution_state changes from empty
    for (let i = 0; i < 15; i++) {
      await new Promise((r) => setTimeout(r, 1000));
      const check = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      if (check?.execution_state) break; // execution_state is set
    }

    // Verify the task exists and has a valid state (not crashed)
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });

    expect(taskData).toBeTruthy();
    expect(taskData.id).toBe(taskId);

    // Valid states: running (processing), idle/completed (done),
    // failed (timeout or error), stopped (user cancelled), or empty (not started)
    console.log(`[agent-test] task state: status=${taskData.status}, execution_state=${taskData.execution_state}, output=${taskData.output || '(none)'}`);
    const validStates = ['', 'running', 'idle', 'completed', 'failed', 'stopped'];
    expect(validStates.includes(taskData.execution_state)).toBe(true);

    // If the task failed, it should have a descriptive error message
    if (taskData.execution_state === 'failed') {
      expect(taskData.output).toBeTruthy();
      expect(taskData.output.length).toBeGreaterThan(0);
    }
  });
});
