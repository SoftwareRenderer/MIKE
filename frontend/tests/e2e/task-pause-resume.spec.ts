import { test, expect } from '@playwright/test';
import { getProjectId, getTaskData } from './helpers';

// Unique suffix for this test run to avoid collisions with stale tasks
const RUN_ID = Date.now();

test.describe.serial('Pause/Resume buttons in In Progress column', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should show pause button for running task in In Progress column', async ({ page }) => {
    const title = `pause-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'stop button test — pause button', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).toBeVisible({ timeout: 10000 });

    const ariaLabel = await moveBtn.getAttribute('aria-label');
    expect(ariaLabel).toMatch(/pause|stop/i);
  });

  test('should show resume button for stopped task in In Progress column', async ({ page }) => {
    const title = `resume-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'stop button test — slow response for pause/resume', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    // Start task
    const startRes = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
      return { status: res.status, data: await res.json() };
    }, { pid: projectId, tid: taskId });
    expect(startRes.status).toBe(200);

    // Wait for task to begin processing
    await page.waitForTimeout(1000);

    // Stop the task
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/stop`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    // Wait for the executor goroutine to process ctx.Done()
    await page.waitForTimeout(2000);

    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('stopped');
    expect(taskData.status).toBe('in_progress');

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).toBeVisible({ timeout: 10000 });

    const ariaLabel = await moveBtn.getAttribute('aria-label');
    expect(ariaLabel).toMatch(/resume/i);
  });

  test('should stop task when pause button is clicked', async ({ page }) => {
    const title = `click-pause-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'stop button test — click pause', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('running');

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const pauseBtn = card.locator('.move-btn');
    await expect(pauseBtn).toBeVisible({ timeout: 10000 });
    await pauseBtn.click();

    await page.waitForTimeout(2000);

    taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status_state).toBe('stopped');
  });

  test('should not show move-to-review button for In Progress tasks', async ({ page }) => {
    const title = `no-review-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'stop button test — no review', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: taskId });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).toBeVisible({ timeout: 10000 });
    const ariaLabel = await moveBtn.getAttribute('aria-label');
    expect(ariaLabel).not.toMatch(/review/i);
  });

  test('should update button from Start to Pause when task is started via chat', async ({ page }) => {
    // Create a task in backlog
    const title = `start-to-pause-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'stop button test — start to pause', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    // Open task detail page (should be in Backlog)
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Click Start Task button
    const startBtn = page.locator('.btn-start-task');
    await expect(startBtn).toBeVisible({ timeout: 10000 });
    await startBtn.click();

    // Wait for task to start, LLM to respond, and board to refresh
    await page.waitForTimeout(3000);

    // Verify task is in In Progress (status_state may be "idle" after LLM text response — conversational mode)
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status).toBe('in_progress');
    expect(['running', 'idle']).toContain(taskData.status_state);

    // Go to board view
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the card in In Progress column and check the button
    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    // Task may be running (pause button) or idle (idle indicator) after LLM response
    const moveBtn = card.locator('.move-btn');
    const idleIndicator = card.locator('.idle-indicator');
    const hasPauseBtn = await moveBtn.isVisible().catch(() => false);
    const hasIdleIndicator = await idleIndicator.isVisible().catch(() => false);
    expect(hasPauseBtn || hasIdleIndicator).toBe(true);

    if (hasPauseBtn) {
      const ariaLabel = await moveBtn.getAttribute('aria-label');
      expect(ariaLabel).toMatch(/pause|stop/i);
    }
    // If idle indicator, that's also valid (task finished LLM response in conversational mode)
  });

  test('should still show cycle-status button for Backlog tasks', async ({ page }) => {
    const title = `backlog-${RUN_ID}`;
    await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'backlog cycle test' }),
      });
      return res.json();
    }, { pid: projectId, title });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).toBeVisible({ timeout: 10000 });
    const ariaLabel = await moveBtn.getAttribute('aria-label');
    expect(ariaLabel).toMatch(/in progress/i);
  });

  test('should show Idle indicator for idle task in In Progress column', async ({ page }) => {
    const title = `idle-${RUN_ID}`;
    // Create task directly in in_progress with idle status_state
    await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'idle indicator test', status: 'in_progress', status_state: 'idle' }),
      });
      return res.json();
    }, { pid: projectId, title });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    // Should show Idle indicator, not a move-to-review button
    const idleIndicator = card.locator('.idle-indicator');
    await expect(idleIndicator).toBeVisible({ timeout: 10000 });
    await expect(idleIndicator).toContainText('Idle');

    // Should NOT show the cycle-status move button
    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).not.toBeVisible();
  });

  test('should show pause/idle button after moving task from Backlog to In Progress', async ({ page }) => {
    const title = `move-${RUN_ID}`;
    const task = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, prompt: 'move to in progress test', llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' }),
      });
      return res.json();
    }, { pid: projectId, title });
    const taskId = task.id;

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the card in Backlog and click the move button
    const card = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await card.hover();
    await page.waitForTimeout(200);

    const moveBtn = card.locator('.move-btn');
    await expect(moveBtn).toBeVisible({ timeout: 10000 });

    // Verify it shows "Move to In Progress" before clicking
    let ariaLabel = await moveBtn.getAttribute('aria-label');
    expect(ariaLabel).toMatch(/in progress/i);

    // Click to move to In Progress (auto-starts the task via backend)
    await moveBtn.click();

    // Wait for the API call and board update
    await page.waitForTimeout(2000);

    // Check task status via API
    let taskData = await getTaskData(page, projectId, taskId);
    expect(taskData.status).toBe('in_progress');

    // Reload board view
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the card (now in In Progress column)
    const cardInProgress = page.locator('.card').filter({ has: page.locator(`.card-title:has-text("${title}")`) });
    await cardInProgress.hover();
    await page.waitForTimeout(200);

    // Should show Pause button or Idle indicator, NOT "Move to Review"
    const moveBtnAfter = cardInProgress.locator('.move-btn');
    const idleIndicator = cardInProgress.locator('.idle-indicator');
    const hasPauseBtn = await moveBtnAfter.isVisible().catch(() => false);
    const hasIdleIndicator = await idleIndicator.isVisible().catch(() => false);
    expect(hasPauseBtn || hasIdleIndicator).toBe(true);

    // If move button is visible, it should NOT be "Move to Review"
    if (hasPauseBtn) {
      ariaLabel = await moveBtnAfter.getAttribute('aria-label');
      expect(ariaLabel).not.toMatch(/review/i);
    }
  });
});
