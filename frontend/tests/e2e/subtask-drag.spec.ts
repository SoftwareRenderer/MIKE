import { test, expect, Page } from '@playwright/test';
import { getProjectId } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Subtask Drag and Drop', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  // Helper: create a task via API with optional parent
  async function createTask(page: Page, prompt: string, parentTaskId?: string): Promise<string> {
    return await page.evaluate(async ({ pid, prompt, parentId }) => {
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

      const body: Record<string, any> = {
        prompt,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
        auto_commit: false,
      };
      if (parentId) {
        body.parent_task_id = parentId;
      }

      const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      });
      const task = await taskRes.json();
      return task.id;
    }, { pid: projectId, prompt, parentId: parentTaskId });
  }

  // Helper: move task to a status via API
  async function moveTask(page: Page, taskId: string, status: string): Promise<void> {
    await page.evaluate(async ({ pid, tid, status }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      });
    }, { pid: projectId, tid: taskId, status });
  }

  // Helper: get task data from API
  async function getTask(page: Page, taskId: string): Promise<Record<string, any>> {
    return await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
  }

  test('should drag a subtask from review to done in a single attempt', async ({ page }) => {
    // Create a parent task and a subtask
    const parentId = await createTask(page, `parent-drag-${Date.now()}`);
    const subId = await createTask(page, `subtask-drag-${Date.now()}`, parentId);

    // Move subtask to review so it appears as a standalone card
    await moveTask(page, subId, 'review');

    // Navigate to the board (close any open task detail)
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Find the subtask card in the review column using data-task-id
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    const subtaskCard = reviewColumn.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard).toBeVisible({ timeout: 5000 });

    // Find the done column
    const doneColumn = page.getByRole('list', { name: 'Done column' });

    // Drag the subtask from review to done in a single attempt
    await subtaskCard.dragTo(doneColumn, { force: true });

    // Verify: subtask should now be in the done column (check via API)
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('done');
    }).toPass({ timeout: 5000, intervals: [200] });
  });

  test('should drag an inline subtask from parent card to another column', async ({ page }) => {
    // Create a parent task and a subtask (both in backlog by default)
    const parentId = await createTask(page, `parent-inline-${Date.now()}`);
    const subId = await createTask(page, `subtask-inline-${Date.now()}`, parentId);

    // Open the subtask via URL — CardDetail auto-expands the parent
    await page.goto(`/?task=${subId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Close CardDetail to return to the board (parent stays expanded via shared store)
    await page.click('.btn-back');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Find the parent card in the backlog column
    const backlogColumn = page.getByRole('list', { name: 'Backlog column' });
    const parentCard = backlogColumn.locator(`[data-task-id="${parentId}"]`);
    await expect(parentCard).toBeVisible({ timeout: 5000 });

    // Wait for the subtask list to appear (parent is expanded)
    await expect(parentCard.locator('.subtask-list')).toBeVisible({ timeout: 5000 });

    // Find the inline subtask card inside the parent
    const inlineSubtask = parentCard.locator(`.subtask-list [data-task-id="${subId}"]`);
    await expect(inlineSubtask).toBeVisible({ timeout: 5000 });

    // Drag the inline subtask to the review column
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    await inlineSubtask.dragTo(reviewColumn, { force: true });

    // Verify: subtask should now be in review
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('review');
    }).toPass({ timeout: 5000, intervals: [200] });
  });

  test('should re-drag a subtask after it was consolidated back to parent column', async ({ page }) => {
    // Create a parent task and a subtask (both in backlog)
    const parentId = await createTask(page, `parent-consolidate-${Date.now()}`);
    const subId = await createTask(page, `subtask-consolidate-${Date.now()}`, parentId);

    // Move subtask to review (standalone card)
    await moveTask(page, subId, 'review');

    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Drag subtask from review back to backlog (same as parent → consolidates)
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    const subtaskCard = reviewColumn.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard).toBeVisible({ timeout: 5000 });
    const backlogColumn = page.getByRole('list', { name: 'Backlog column' });
    await subtaskCard.dragTo(backlogColumn, { force: true });

    // Verify subtask is back in backlog (consolidated into parent)
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('backlog');
    }).toPass({ timeout: 5000, intervals: [200] });

    // Now move it to review again via API so we can drag it again
    await moveTask(page, subId, 'review');

    // Drag from review to done (should work in a single attempt after consolidation)
    const reviewColumn2 = page.getByRole('list', { name: 'Review column' });
    const subtaskCard2 = reviewColumn2.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard2).toBeVisible({ timeout: 5000 });
    const doneColumn = page.getByRole('list', { name: 'Done column' });
    await subtaskCard2.dragTo(doneColumn, { force: true });

    // Verify subtask is now in done
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('done');
    }).toPass({ timeout: 5000, intervals: [200] });
  });

  test('should block dragging a subtask to Backlog when parent is In Progress', async ({ page }) => {
    // Create a parent task and a subtask
    const parentId = await createTask(page, `parent-block-${Date.now()}`);
    const subId = await createTask(page, `subtask-block-${Date.now()}`, parentId);

    // Move subtask to Review first (so it's standalone)
    await moveTask(page, subId, 'review');
    // Now move parent to In Progress (parent stays in in_progress, subtask stays in review)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
    }, { pid: projectId, tid: parentId });

    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Find the subtask card in the review column
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    const subtaskCard = reviewColumn.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard).toBeVisible({ timeout: 5000 });

    // Attempt to drag the subtask to Backlog — should be blocked
    const backlogColumn = page.getByRole('list', { name: 'Backlog column' });
    await subtaskCard.dragTo(backlogColumn, { force: true });

    // Verify: subtask should still be in review (not moved)
    const task = await getTask(page, subId);
    expect(task.status).toBe('review');
  });

  test('should allow dragging a subtask to Review/Done when parent is In Progress', async ({ page }) => {
    // Create a parent task and a subtask
    const parentId = await createTask(page, `parent-allow-${Date.now()}`);
    const subId = await createTask(page, `subtask-allow-${Date.now()}`, parentId);

    // Move subtask to Review first
    await moveTask(page, subId, 'review');
    // Now move parent to In Progress
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
    }, { pid: projectId, tid: parentId });

    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Find the subtask card in the review column
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    const subtaskCard = reviewColumn.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard).toBeVisible({ timeout: 5000 });

    // Drag the subtask to Done — should be allowed
    const doneColumn = page.getByRole('list', { name: 'Done column' });
    await subtaskCard.dragTo(doneColumn, { force: true });

    // Verify: subtask should now be in done
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('done');
    }).toPass({ timeout: 5000, intervals: [200] });
  });

  test('moving a subtask to In Progress does not move siblings in review/done', async ({ page }) => {
    // Create a parent task and two subtasks
    const parentId = await createTask(page, `parent-sibling-isolation-${Date.now()}`);
    const subA = await createTask(page, `sub-a-${Date.now()}`, parentId);
    const subB = await createTask(page, `sub-b-${Date.now()}`, parentId);

    // Set all three to in_progress via direct PUT (bypasses move endpoint's executor/auto-start)
    await page.evaluate(async ({ pid, tasks }) => {
      for (const tid of tasks) {
        await fetch(`/api/projects/${pid}/tasks/${tid}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ status: 'in_progress', status_state: 'idle' }),
        });
      }
    }, { pid: projectId, tasks: [parentId, subA, subB] });

    let taskA = await getTask(page, subA);
    let taskB = await getTask(page, subB);
    expect(taskA.status).toBe('in_progress');
    expect(taskB.status).toBe('in_progress');

    // Move subtask A to Review (wait for response)
    await page.evaluate(async ({ pid, tid, status }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      });
      await res.json();
    }, { pid: projectId, tid: subA, status: 'review' });
    taskA = await getTask(page, subA);
    expect(taskA.status).toBe('review');

    // Move subtask B to Done (wait for response)
    await page.evaluate(async ({ pid, tid, status }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      });
      await res.json();
    }, { pid: projectId, tid: subB, status: 'done' });
    taskB = await getTask(page, subB);
    expect(taskB.status).toBe('done');

    // Move subtask A back to In Progress — this is the bug trigger
    await page.evaluate(async ({ pid, tid, status }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status }),
      });
      await res.json();
    }, { pid: projectId, tid: subA, status: 'in_progress' });

    // Verify: A is in_progress, B should STAY in done (not moved with A)
    taskA = await getTask(page, subA);
    taskB = await getTask(page, subB);
    expect(taskA.status).toBe('in_progress');
    expect(taskB.status).toBe('done');
  });

  test('should drag a subtask from review to backlog in a single attempt', async ({ page }) => {
    // Create a parent task and a subtask
    const parentId = await createTask(page, `parent-drag2-${Date.now()}`);
    const subId = await createTask(page, `subtask-drag2-${Date.now()}`, parentId);

    // Move subtask to review so it appears as a standalone card
    await moveTask(page, subId, 'review');

    // Navigate to the board
    await page.goto('/');
    await page.waitForSelector('.board', { timeout: 10000 });

    // Find the subtask card in the review column using data-task-id
    const reviewColumn = page.getByRole('list', { name: 'Review column' });
    const subtaskCard = reviewColumn.locator(`[data-task-id="${subId}"]`);
    await expect(subtaskCard).toBeVisible({ timeout: 5000 });

    // Find the backlog column
    const backlogColumn = page.getByRole('list', { name: 'Backlog column' });

    // Drag the subtask from review to backlog
    await subtaskCard.dragTo(backlogColumn, { force: true });

    // Verify: subtask should now be in backlog
    await expect(async () => {
      const task = await getTask(page, subId);
      expect(task.status).toBe('backlog');
    }).toPass({ timeout: 5000, intervals: [200] });
  });
});
