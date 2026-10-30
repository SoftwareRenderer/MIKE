import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, findProviderByRef, isolatedProject, gotoProject, clearBoardExcept, waitForTaskState } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


/**
 * Multi-select (Ctrl+Click, selection bar, delete, start) in the Board Overview panel that the
 * Task Detail view opens — the same interactions as the main board.
 */
test.describe.serial('Task View Board Overview multi-select', () => {
  let projectId = '';
  let taskId1 = '';
  let taskId2 = '';
  let taskId3 = '';

/**
 * Multi-select: parent task selection recursively selects all subtasks.
 */
test.describe.serial('Multi-select parent/subtask recursion', () => {
  let projectId = '';
  let parentTaskId = '';
  let childTaskId1 = '';
  let childTaskId2 = '';
  let grandchildTaskId = '';

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
    projectId = await getProjectId(page);
    // This block creates exactly the tasks it selects, so start from an empty board:
    // whatever the previous spec on this worker left behind (running tasks especially)
    // cannot be deleted through the multi-select bar the same way.
    await clearBoardExcept(page, projectId);

    // Create a parent task
    parentTaskId = await createTaskViaApi(page, projectId, `parent-${Date.now()}`);
    await page.waitForSelector(`[data-task-id="${parentTaskId}"]`, { timeout: 10000 });

    // Create two child tasks via the API (using parent_task_id)
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const defaultProvider = findProviderByRef(config.providers || [], config.worker_provider);

    // Create first child
    const child1Result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `child-1-${Date.now()}`,
        parent_task_id: parentTaskId,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    childTaskId1 = child1Result.data?.id;

    // Create second child
    const child2Result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `child-2-${Date.now()}`,
        parent_task_id: parentTaskId,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    childTaskId2 = child2Result.data?.id;

    // Create a grandchild under childTaskId1
    const grandchildResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `grandchild-${Date.now()}`,
        parent_task_id: childTaskId1,
        llama_server_url: defaultProvider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    grandchildTaskId = grandchildResult.data?.id;

    // Wait for all tasks to appear on the board
    await expect(async () => {
      const tasks = await page.evaluate(async (pid) => {
        const res = await fetch(`/api/projects/${pid}/board`);
        return await res.json();
      }, projectId);
      expect(tasks.tasks.length).toBeGreaterThanOrEqual(4);
    }).toPass({ timeout: 10000 });

    // Move children to in_progress so they aren't consolidated with parent (backlog)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${childTaskId1}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${childTaskId2}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    // Grandchild follows its parent
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${grandchildTaskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Navigate to the parent task detail view
    await navigateToTask(page, parentTaskId, projectId);

    // Wait for board overview to render
    await expect(async () => {
      const count = await page.locator('.column-context .card').count();
      expect(count).toBeGreaterThanOrEqual(1);
    }).toPass({ timeout: 10000 });
  });

  test('should select all subtasks when Ctrl+clicking a parent', async ({ page }) => {

    // Click on the title text to avoid hitting nested child cards
    const parentTitle = page.locator(`.column-context [data-task-id="${parentTaskId}"] .card-title-text`).first();
    await expect(parentTitle).toBeVisible();

    // Ctrl+Click the parent task title
    await parentTitle.click({ modifiers: ['Control'] });

    // Multi-select bar should appear with count >= 4 (parent + 2 children + grandchild)
    const bar = page.locator('.multi-select-bar');
    await expect(bar).toBeVisible();

    await expect(page.locator(`.column-context [data-task-id="${parentTaskId}"]`).first()).toHaveClass(/multi-selected/);
    // Children should also be selected (check first occurrence — nested under parent)
    await expect(page.locator(`.column-context [data-task-id="${childTaskId1}"]`).first()).toHaveClass(/multi-selected/);
    await expect(page.locator(`.column-context [data-task-id="${childTaskId2}"]`).first()).toHaveClass(/multi-selected/);
    // Grandchild should also be selected (recursive)
    await expect(page.locator(`.column-context [data-task-id="${grandchildTaskId}"]`).first()).toHaveClass(/multi-selected/);
  });

  test('should deselect all subtasks when Ctrl+clicking a selected parent', async ({ page }) => {

    const parentTitle = page.locator(`.column-context [data-task-id="${parentTaskId}"] .card-title-text`).first();
    await expect(parentTitle).toBeVisible();
    const parentCard = page.locator(`.column-context [data-task-id="${parentTaskId}"]`).first();

    // Ctrl+Click the parent to select (selects all)
    await parentTitle.click({ modifiers: ['Control'] });
    await expect(page.locator('.multi-select-bar')).toBeVisible();

    // Ctrl+Click the parent again to deselect
    await parentTitle.click({ modifiers: ['Control'] });

    // All should be deselected
    await expect(parentCard).not.toHaveClass(/multi-selected/);
    await expect(page.locator(`.column-context [data-task-id="${childTaskId1}"]`).first()).not.toHaveClass(/multi-selected/);
    await expect(page.locator(`.column-context [data-task-id="${childTaskId2}"]`).first()).not.toHaveClass(/multi-selected/);
  });
});

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    // The board, not `.card`: the nested block above deletes every task it created, so a
    // card-first wait dead-ends on an empty board. The tasks this block needs come next.
    await page.waitForSelector('.board', { timeout: 10000 });
    projectId = await getProjectId(page);
    // Select exactly the three tasks created below — nothing else may be on the board.
    await clearBoardExcept(page, projectId);

    // Create 3 tasks — all in Backlog
    taskId1 = await createTaskViaApi(page, projectId, `taskview-ms-${Date.now()}-1`);
    taskId2 = await createTaskViaApi(page, projectId, `taskview-ms-${Date.now()}-2`);
    taskId3 = await createTaskViaApi(page, projectId, `taskview-ms-${Date.now()}-3`);

    // Wait for all 3 to appear on the board
    await page.waitForSelector(`[data-task-id="${taskId3}"]`, { timeout: 10000 });

    // Navigate to task detail view (opens taskId1)
    await navigateToTask(page, taskId1, projectId);

    // Wait for the board overview sidebar to render cards
    await expect(async () => {
      const count = await page.locator('.column-context .card').count();
      expect(count).toBeGreaterThanOrEqual(3);
    }).toPass({ timeout: 10000 });
  });

  test('should select tasks with Ctrl+Click in board overview', async ({ page }) => {

    // Ctrl+Click on taskId2 and taskId3 in the board overview
    const card2 = page.locator(`.column-context [data-task-id="${taskId2}"]`);
    const card3 = page.locator(`.column-context [data-task-id="${taskId3}"]`);

    await card2.click({ modifiers: ['Control'] });
    await card3.click({ modifiers: ['Control'] });

    // Multi-select bar should appear
    const bar = page.locator('.multi-select-bar');
    await expect(bar).toBeVisible();

    // Should show count of 2
    await expect(bar).toContainText('2');
  });

  test('should move the selected tasks to the chosen column on confirm', async ({ page }) => {

    const card2 = page.locator(`.column-context [data-task-id="${taskId2}"]`);
    const card3 = page.locator(`.column-context [data-task-id="${taskId3}"]`);
    await card2.click({ modifiers: ['Control'] });
    await card3.click({ modifiers: ['Control'] });

    await page.locator('.multi-select-bar .bar-btn-move').click();
    await page.locator('.move-item', { hasText: 'Review' }).click();
    await page.locator('.modal-footer .btn-confirm').click();

    await waitForTaskState(page, projectId, taskId2, t => t.status === 'review');
    await waitForTaskState(page, projectId, taskId3, t => t.status === 'review');

    // The selection is gone with the batch.
    await expect(page.locator('.multi-select-bar')).not.toBeVisible();
  });

  test('should clear selection when clicking clear button', async ({ page }) => {

    const card2 = page.locator(`.column-context [data-task-id="${taskId2}"]`);
    const card3 = page.locator(`.column-context [data-task-id="${taskId3}"]`);

    await card2.click({ modifiers: ['Control'] });
    await card3.click({ modifiers: ['Control'] });

    // Multi-select bar should be visible
    const bar = page.locator('.multi-select-bar');
    await expect(bar).toBeVisible();

    // Click clear button
    const clearBtn = page.locator('.bar-btn-clear');
    await clearBtn.click();

    // Bar should disappear
    await expect(bar).not.toBeVisible();

    // Selection highlight should be removed
    await expect(card2).not.toHaveClass(/multi-selected/);
    await expect(card3).not.toHaveClass(/multi-selected/);
  });

  test('should clear selection on Escape without closing task detail', async ({ page }) => {

    const card2 = page.locator(`.column-context [data-task-id="${taskId2}"]`);
    const card3 = page.locator(`.column-context [data-task-id="${taskId3}"]`);

    await card2.click({ modifiers: ['Control'] });
    await card3.click({ modifiers: ['Control'] });

    // Multi-select bar should be visible
    const bar = page.locator('.multi-select-bar');
    await expect(bar).toBeVisible();

    // Press Escape — should clear selection, NOT close task detail
    await page.keyboard.press('Escape');

    // Selection should be cleared
    await expect(bar).not.toBeVisible();
    await expect(card2).not.toHaveClass(/multi-selected/);
    await expect(card3).not.toHaveClass(/multi-selected/);

    // Task detail page should still be open
    await expect(page.locator('.task-detail-page')).toBeVisible();
  });
});
