import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

// Real-time task state updates via WebSocket — both Board and CardDetail views.

test.describe('Real-time Execution State Updates', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should update CardDetail badge when task execution_state changes via API', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `rt-update-${Date.now()}`);

    // Move to in_progress
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Wait for board to reflect status change
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('in_progress');
    }).toPass({ timeout: 5000 });

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the "Idle" badge in the task detail header
    await expect(async () => {
      const header = page.locator('.task-detail-page > div > div > div');
      const badge = header.locator('.badge').first();
      const text = await badge.textContent();
      expect(text).toContain('Idle');
    }).toPass({ timeout: 5000 });

    // Set execution_state to "stopped" via API (mimics loop-cancellation behavior)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
      method: 'PUT',
      body: { execution_state: 'stopped' },
    });

    // Wait for the "Stopped" badge via WebSocket
    await expect(async () => {
      const header = page.locator('.task-detail-page > div > div > div');
      const badge = header.locator('.badge').first();
      const text = await badge.textContent();
      expect(text).toContain('Stopped');
    }).toPass({ timeout: 10000 });
  });

  test('should update Board card badge when task execution_state changes via API', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `rt-board-${Date.now()}`);

    // Move to in_progress
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Wait for board to reflect status change
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('in_progress');
    }).toPass({ timeout: 5000 });

    // Navigate to the task, then go back to board
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('button[aria-label="Back to board (Esc)"]');
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 5000 });

    // Set execution_state to "stopped" via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
      method: 'PUT',
      body: { execution_state: 'stopped' },
    });

    // Wait for the "Stopped" badge via WebSocket (status badge, not model-badge)
    const stoppedCard = page.locator(`.card[data-task-id="${taskId}"]`);
    await expect(async () => {
      const badge = stoppedCard.locator('.badge:not(.model-badge)');
      const text = await badge.textContent();
      expect(text).toContain('Stopped');
    }).toPass({ timeout: 10000 });
  });

  test('should update Board card badge when execution_state changes to failed', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `rt-failed-${Date.now()}`);

    // Move to in_progress
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Wait for board to reflect status change
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('in_progress');
    }).toPass({ timeout: 5000 });

    // Navigate to the task, then go back to board
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('button[aria-label="Back to board (Esc)"]');
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator(`.card[data-task-id="${taskId}"]`)).toBeVisible({ timeout: 5000 });

    // Set execution_state to "failed" via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
      method: 'PUT',
      body: { execution_state: 'failed' },
    });

    // Wait for the "Failed" badge via WebSocket (status badge, not model-badge)
    const failedCard = page.locator(`.card[data-task-id="${taskId}"]`);
    await expect(async () => {
      const badge = failedCard.locator('.badge:not(.model-badge)');
      const text = await badge.textContent();
      expect(text).toContain('Failed');
    }).toPass({ timeout: 10000 });
  });
});
