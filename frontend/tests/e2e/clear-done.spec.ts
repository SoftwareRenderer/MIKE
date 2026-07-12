import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

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

    // Move task to done via API (CSRF-aware)
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    expect(moveResult.status).toBe(200);

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
    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Check done count via API
    const doneCount = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });

    // If there are done tasks, the clear button should be visible
    // If there are no done tasks, it should not be visible
    const clearBtn = page.locator('.clear-btn');
    if (doneCount === 0) {
      await expect(clearBtn).not.toBeVisible();
    } else {
      await expect(async () => {
        await expect(clearBtn).toBeVisible();
      }).toPass({ timeout: 5000 });
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
    // Count done tasks before creating new ones
    const doneCountBase = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });

    // Create multiple tasks and move them to done
    const taskId1 = await createTaskViaApi(page, projectId, `clear-done-delete-1-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `clear-done-delete-2-${Date.now()}`);

    // Move tasks to done via API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId1}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId2}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    await page.goto('/');
    await page.waitForSelector('.card');

    // Verify done count increased by 2
    const doneCountBefore = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountBefore).toBe(doneCountBase + 2);

    // Click clear button to trigger confirmation dialog
    await page.click('.clear-btn');

    // Wait for confirmation dialog and accept
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.click('.confirm-dialog .btn-confirm');

    // Wait for done tasks to be deleted (replaces waitForTimeout(1000) + navigate)
    await page.goto('/');
    await page.waitForSelector('.card');

    // All done tasks should be deleted
    const doneCountAfter = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'done').length;
    }, { pid: projectId });
    expect(doneCountAfter).toBe(0);
  });

  test('should not reappear in Review after move-to-done then clear', async ({ page }) => {
    // Exact reproduction: task in Review -> move to Done -> click "Clear all" -> task gone.
    // Regression: tasks were reappearing in Review column after clear-done
    // due to a race between refreshBoard() and task_deleted WebSocket events.
    const taskId = await createTaskViaApi(page, projectId, `clear-done-no-reappear-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Step 1: Move task to Review via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Step 2: Move task to Done via API (simulates clicking "Move to Done" on the card)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });

    // Step 3: Navigate to board and verify task is in Done
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(task.status).toBe('done');
    }).toPass({ timeout: 10000 });

    // Step 4: Click "Clear all" in Done column and confirm
    await page.click('.clear-btn');
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.click('.confirm-dialog .btn-confirm');

    // Step 5: Wait for the task card to disappear from the board (no page reload)
    await expect(page.locator(`.card[data-task-id="${taskId}"]`)).not.toBeVisible({ timeout: 10000 });

    // Step 6: Verify via API: task does not exist on the server
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        const board = await res.json();
        return board.tasks.find((t: any) => t.id === tid);
      }, { pid: projectId, tid: taskId });
      expect(task).toBeUndefined();
    }).toPass({ timeout: 10000 });
  });
});
