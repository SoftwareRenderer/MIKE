import { test, expect } from '@playwright/test';
import { getProjectId, getTaskData } from './helpers';

test.describe.serial('Start Task Moves to In Progress', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should move task from backlog to in_progress when Start Task is clicked', async ({ page }) => {
    // Create a task via API (starts in backlog)
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `start-move-test-${Date.now()}`, prompt: 'hello' }),
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
    // The startTask() call returns the updated task and the frontend updates the board store
    await expect(async () => {
      taskData = await getTaskData(page, projectId, taskId);
      expect(taskData.status).toBe('in_progress');
    }).toPass({ timeout: 10000 });

    // Also verify the status_state is set (running or idle depending on LLM response)
    expect(['running', 'idle', 'stopped', 'failed', 'completed']).toContain(taskData.status_state);
  });

  test('should reflect in_progress in the board column after start', async ({ page }) => {
    // Create a task via API
    const task = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title: `column-move-test-${Date.now()}`, prompt: 'hi' }),
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

    // Close the task detail and go back to board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 5000 });

    // The task should appear in the "In Progress" column, not "Backlog"
    // Check via API that the board reflects the new status
    const boardData = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return res.json();
    }, { pid: projectId });

    const taskOnBoard = boardData.tasks.find((t: any) => t.id === taskId);
    expect(taskOnBoard).toBeDefined();
    expect(taskOnBoard.status).toBe('in_progress');
  });
});
