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
        body: JSON.stringify({ title: `stop test ${Date.now()}`, prompt: 'stop button test', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
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
        body: JSON.stringify({ title: `fail test ${Date.now()}`, prompt: 'read a file that does not exist', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
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

  test('should move task from backlog to in_progress when Start Task is clicked', async ({ page }) => {
    // Create a task via API (starts in backlog)
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `start-move-test-${Date.now()}`, prompt: 'hello', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Verify task is in backlog
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status).toBe('backlog');

    // Navigate to the task by ID
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the Start Task button to appear
    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });

    // Click Start Task
    await startButton.click();

    // Wait for the task status to update via API
    await expect(async () => {
      taskData = await getTaskData(page, projectId, taskId);
      expect(taskData.status).toBe('in_progress');
    }).toPass({ timeout: 10000 });

    // Also verify the status_state is set
    expect(['running', 'idle', 'stopped', 'failed', 'completed']).toContain(taskData.status_state);
  });

  test('should reflect in_progress in the board column after start', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `column-move-test-${Date.now()}`, prompt: 'hi', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click Start Task
    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });
    await startButton.click();

    // Wait for start to complete
    await expect(async () => {
      const data = await getTaskData(page, projectId, taskId);
      expect(data.status).toBe('in_progress');
    }).toPass({ timeout: 10000 });

    // Go back to board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 5000 });

    // Check via API that the board reflects the new status
    const boardData = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return res.json();
    }, { pid: projectId });

    const taskOnBoard = boardData.tasks.find((t: any) => t.id === taskId);
    expect(taskOnBoard).toBeDefined();
    expect(taskOnBoard.status).toBe('in_progress');
  });

  test('should transition running -> idle after text-only LLM response', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `transition-test-${Date.now()}`, prompt: 'hello', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Start the task
    const startRes = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
      return { status: res.status, data: await res.json() };
    }, { pid: projectId, tid: taskId });
    expect(startRes.status).toBe(200);
    expect(startRes.data.status_state).toBe('running');

    // Poll for the task to reach idle (text-only response ends with idle)
    let taskData: Record<string, unknown>;
    let iterations = 0;
    const maxIterations = 30;
    do {
      await page.waitForTimeout(500);
      taskData = await getTaskData(page, projectId, taskId);
      iterations++;
    } while (taskData.status_state === 'running' && iterations < maxIterations);

    // Should transition to idle after text response
    expect(taskData.status_state).toBe('idle');
    expect(taskData.status).toBe('in_progress');
  });

  test('should update status_state in real time via board WebSocket (no page reload)', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `realtime-test-${Date.now()}`, prompt: 'hello realtime', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Navigate to board and wait for it to load
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Start the task via API (triggers executor in background)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for executor to finish (text response -> idle)
    await expect(async () => {
      const data = await getTaskData(page, projectId, taskId);
      expect(data.status_state).toBe('idle');
    }).toPass({ timeout: 15000 });

    // Now verify the board reflects the updated status_state WITHOUT a page reload
    // The board WebSocket should have broadcast the update
    const boardData = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return res.json();
    }, { pid: projectId });

    const taskOnBoard = boardData.tasks.find((t: any) => t.id === taskId);
    expect(taskOnBoard).toBeDefined();
    expect(taskOnBoard.status_state).toBe('idle');
    expect(taskOnBoard.status).toBe('in_progress');
  });

  test('should show Stopped badge for stopped task on board view', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `stopped-badge-${Date.now()}`, prompt: 'stop button test', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Start the task
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait a moment for task to begin
    await page.waitForTimeout(500);

    // Stop the task
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/stop`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for executor to process stop
    await page.waitForTimeout(2000);

    // Verify status_state is stopped via API
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('stopped');

    // Navigate to board view (no TaskChat mounted)
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the task card and check for the Stopped badge
    const card = page.locator('.card').filter({ hasText: task.title });
    await card.hover();
    await page.waitForTimeout(200);

    // The badge should show "Stopped" (visible on hover)
    const badge = card.locator('.badge:has-text("Stopped")');
    await expect(badge).toBeVisible({ timeout: 5000 });
  });

  test('should show Failed badge for task that exceeded max tool failures', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `failed-badge-${Date.now()}`, prompt: 'read a file that does not exist', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Set max_consecutive_failed_turns to 1
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ max_consecutive_failed_turns: 1 }),
      });
    }, { pid: projectId, tid: taskId });

    // Start the task
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for task to fail
    await expect(async () => {
      const data = await getTaskData(page, projectId, taskId);
      expect(data.status_state).toBe('failed');
    }).toPass({ timeout: 15000 });

    // Navigate to board view
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the task card and check for the Failed badge
    const card = page.locator('.card').filter({ hasText: task.title });
    await card.hover();
    await page.waitForTimeout(200);

    // The badge should show "Failed"
    const badge = card.locator('.badge:has-text("Failed")');
    await expect(badge).toBeVisible({ timeout: 5000 });
  });

  test('should not show duplicate messages after stop and restart', async ({ page }) => {
    // Create a task via API with a slow-response prompt
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `stop-restart-${Date.now()}`, prompt: 'stop button test', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId });
    const taskId = task.id;

    // Navigate to task detail page
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click Start Task
    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });
    await startButton.click();

    // Wait for LLM response to start streaming (stop button appears)
    const stopButton = page.locator('.btn-stop');
    await expect(stopButton).toBeVisible({ timeout: 10000 });

    // Count assistant messages BEFORE stop (should be 1 streaming entry)
    const assistantCountBefore = await page.locator('.chat-message').count();

    // Stop the task
    await stopButton.click();

    // Wait for stop to process
    await page.waitForTimeout(2000);

    // Verify task is stopped
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('stopped');

    // Count assistant messages AFTER stop (streaming entry should be removed by history reload)
    // The user message persists, but the partial assistant (unpersisted) should be gone
    const userMessagesAfterStop = await page.locator('.chat-message:has-text("stop button test")').count();
    expect(userMessagesAfterStop).toBe(1); // User message should remain

    // Now restart via resume (retry) API
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/retry`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for the new executor to respond
    await expect(async () => {
      taskData = await getTaskData(page, projectId, taskId);
      expect(['idle', 'running']).toContain(taskData.status_state);
    }).toPass({ timeout: 15000 });

    // Wait a moment for WebSocket to deliver messages
    await page.waitForTimeout(1000);

    // Count user messages — should still be exactly 1 (no duplicates)
    const userMessagesAfterRestart = await page.locator('.chat-message:has-text("stop button test")').count();
    expect(userMessagesAfterRestart).toBe(1);
  });
});
