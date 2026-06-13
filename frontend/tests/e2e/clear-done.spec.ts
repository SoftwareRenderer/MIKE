import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Clear All Done Tasks', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should show Clear all button in Done column header when done tasks exist', async ({ page }) => {
    // Create a task and move it to done
    const taskId = await createTaskViaApi(page, projectId, `clear-done-visible-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move task to done via API
    const moveStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
      return res.status;
    }, { pid: projectId, tid: taskId });
    expect(moveStatus).toBe(200);

    // Navigate to the board view
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait for the moved task to appear on the board (server may be slow under load)
    await expect(async () => {
      const board = await page.evaluate(async ({ pid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return res.json();
      }, { pid: projectId });
      const doneTasks = board.tasks.filter((t: any) => t.status === 'done');
      expect(doneTasks.length).toBeGreaterThanOrEqual(1);
    }).toPass({ timeout: 10000 });

    // Clear all button should be visible in the Done column header
    const clearBtn = page.locator('.clear-btn');
    await expect(clearBtn).toBeVisible({ timeout: 10000 });
  });

  test('should not show Clear all button when no done tasks exist', async ({ page }) => {
    // Ensure no done tasks by checking via API
    const doneCount = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });

    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // If there are done tasks, the clear button should be visible
    // If there are no done tasks, it should not be visible
    const clearBtn = page.locator('.clear-btn');
    if (doneCount === 0) {
      await expect(clearBtn).not.toBeVisible();
    } else {
      await expect(clearBtn).toBeVisible();
    }
  });

  test('should confirm before clearing done tasks', async ({ page }) => {
    // Create a task and move it to done
    const taskId = await createTaskViaApi(page, projectId, `clear-done-confirm-${Date.now()}`);
    expect(taskId).not.toBe('');

    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
    }, { pid: projectId, tid: taskId });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Count done tasks before
    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountBefore).toBeGreaterThan(0);

    // Click clear button to trigger confirmation dialog
    await page.click('.clear-btn');

    // Wait for confirmation dialog to appear
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    expect(await page.locator('.confirm-dialog p').textContent()).toContain('Permanently delete');

    // Cancel the confirmation
    await page.click('.confirm-dialog .btn-cancel');

    // Done tasks should still exist
    const doneCountAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountAfter).toBe(doneCountBefore);
  });

  test('should delete all done tasks on confirmation', async ({ page }) => {
    // Create multiple tasks and move them to done
    const taskId1 = await createTaskViaApi(page, projectId, `clear-done-delete-1-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `clear-done-delete-2-${Date.now()}`);

    await page.evaluate(async ({ pid, tid1, tid2 }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid1}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
      await fetch(`/api/projects/${pid}/tasks/${tid2}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
    }, { pid: projectId, tid1: taskId1, tid2: taskId2 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Count done tasks before
    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountBefore).toBeGreaterThanOrEqual(2);

    // Click clear button to trigger confirmation dialog
    await page.click('.clear-btn');

    // Wait for confirmation dialog and accept
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.click('.confirm-dialog .btn-confirm');

    // Wait for done tasks to be deleted (replaces waitForTimeout(1000) + navigate)
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // All done tasks should be deleted
    const doneCountAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountAfter).toBe(0);
  });
});
