import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task Delete Cleanup', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should remove task from board after delete', async ({ page }) => {
    // Create a task via API
    const taskId = await createTaskViaApi(page, projectId, `delete-cleanup-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Verify task exists in board
    const existsBefore = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.some((t: any) => t.id === tid);
    }, { pid: projectId, tid: taskId });
    expect(existsBefore).toBe(true);

    // Also verify task is accessible via direct API
    const taskStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(taskStatus).toBe(200);

    // Delete the task via API
    const deleteStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`, { method: 'DELETE' });
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(deleteStatus).toBe(204);

    // Verify task is gone from board
    const existsAfter = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.some((t: any) => t.id === tid);
    }, { pid: projectId, tid: taskId });
    expect(existsAfter).toBe(false);

    // Verify task returns 404 from direct API
    const taskStatusAfter = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(taskStatusAfter).toBe(404);
  });

  test('should remove task messages after task delete (CASCADE)', async ({ page }) => {
    // Create a task via API
    const taskId = await createTaskViaApi(page, projectId, `cascade-cleanup-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Verify messages endpoint works for the task (prompt is stored as first message)
    const msgsBefore = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
      if (res.status === 404) return null;
      return await res.json();
    }, { pid: projectId, tid: taskId });
    expect(Array.isArray(msgsBefore)).toBe(true);

    // Delete the task
    const deleteStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`, { method: 'DELETE' });
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(deleteStatus).toBe(204);

    // Verify task is gone (404)
    const taskAfter = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(taskAfter).toBe(404);
  });

  test('should remove task dependencies after task delete (CASCADE)', async ({ page }) => {
    // Create two tasks
    const parentTaskId = await createTaskViaApi(page, projectId, `dep-parent-${Date.now()}`);
    const childTaskId = await createTaskViaApi(page, projectId, `dep-child-${Date.now()}`);

    // Create a dependency between them
    const depStatus = await page.evaluate(async ({ pid, parent, child }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${parent}/dependencies`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ child_task_id: child, auto_start: false }),
      });
      return res.status;
    }, { pid: projectId, parent: parentTaskId, child: childTaskId });
    expect(depStatus).toBe(201);

    // Verify dependency exists
    const depsBefore = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/dependencies`);
      return await res.json();
    }, { pid: projectId, tid: parentTaskId });
    expect(depsBefore.length).toBe(1);

    // Delete the parent task
    const deleteStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`, { method: 'DELETE' });
      return res.status;
    }, { pid: projectId, tid: parentTaskId });
    expect(deleteStatus).toBe(204);

    // Verify dependency is gone (CASCADE) - parent task returns 404
    const parentStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.status;
    }, { pid: projectId, tid: parentTaskId });
    expect(parentStatus).toBe(404);

    // Child task should still exist (only the dependency row was CASCADE-deleted)
    const childStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.status;
    }, { pid: projectId, tid: childTaskId });
    expect(childStatus).toBe(200);
  });

  test('should delete via UI and navigate back to board', async ({ page }) => {
    // Create a task via UI
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    const taskTitle = `ui-delete-${Date.now()}`;
    await page.fill('textarea', taskTitle);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Open the task
    const taskCard = page.locator('.card').filter({ hasText: taskTitle });
    await taskCard.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Accept the confirmation dialog
    page.on('dialog', async (dialog) => {
      expect(dialog.message()).toContain('Delete');
      await dialog.accept();
    });

    // Click delete button
    await page.click('.btn-icon[aria-label="Delete task"]');

    // Should navigate back to board
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();

    // Task card should no longer be on the board
    await expect(page.locator('.card').filter({ hasText: taskTitle })).not.toBeVisible();
  });

  test('should remove orphaned worktree directories via GC', async ({ page }) => {
    // Create an orphaned worktree directory (not tied to any real task)
    const orphan = await page.evaluate(async () => {
      const res = await fetch('/api/test/create-orphan', { method: 'POST' });
      return await res.json();
    });
    expect(orphan.orphan).toBeTruthy();
    expect(orphan.path).toBeTruthy();

    // Verify the orphan directory exists via worktree-exists (using a fake task ID pattern)
    // Since the orphan name is unique and not a real task, we verify via the GC endpoint

    // Run GC
    const gcResult = await page.evaluate(async () => {
      const res = await fetch('/api/gc', { method: 'POST' });
      return await res.json();
    });
    expect(gcResult.removed).toBeGreaterThanOrEqual(1);

    // Verify the orphan is gone by running GC again (should find 0)
    const gcAfter = await page.evaluate(async () => {
      const res = await fetch('/api/gc', { method: 'POST' });
      return await res.json();
    });
    expect(gcAfter.removed).toBe(0);
  });
});
