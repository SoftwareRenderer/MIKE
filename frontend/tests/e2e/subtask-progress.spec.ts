import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

// Helper: create a subtask via API (CSRF-aware)
async function createSubtaskViaApi(
  page,
  projectId: string,
  parentTaskId: string,
  prompt: string
): Promise<string> {
  const config = await page.evaluate(async () => {
    const res = await fetch('/api/config');
    return res.json();
  });
  const defaultProvider = config.providers?.find(
    (p: any) => p.name === config.default_provider
  );
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: {
      prompt,
      parent_task_id: parentTaskId,
      llama_server_url: defaultProvider?.url || '',
      model_name: defaultProvider?.default_model || '',
    },
  });
  return result.data.id;
}

// Helper: move a task to a status via API
async function moveTaskViaApi(
  page,
  projectId: string,
  taskId: string,
  status: string
): Promise<void> {
  const result = await csrfFetch(
    page,
    `/api/projects/${projectId}/tasks/${taskId}/move`,
    {
      method: 'POST',
      body: { status },
    }
  );
  if (!result.ok) {
    throw new Error(`moveTaskViaApi: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
}

// Helper: wait for the board to be visible
async function waitForBoard(page): Promise<void> {
  await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
}

// Helper: get the subtask progress bar for a specific parent card
function getProgressForCard(page, parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-bar`);
}

// Helper: get the progress count text for a specific parent card
function getProgressCountForCard(page, parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-count`);
}

// Helper: get the progress fill element for a specific parent card
function getProgressFillForCard(page, parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-fill`);
}

test.describe.configure({ mode: 'serial' });

test.describe('SubtaskProgress component', () => {
  let projectId: string;
  let parentId: string;
  let subtaskIds: string[] = [];

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('progress indicator appears for parent tasks with subtasks', async ({ page }) => {
    projectId = await getProjectId(page);

    // Create a parent task
    const parentTitle = `progress-parent-${Date.now()}`;
    parentId = await createTaskViaApi(page, projectId, parentTitle);

    // Create 3 subtasks under the parent
    subtaskIds = [];
    for (let i = 0; i < 3; i++) {
      const subId = await createSubtaskViaApi(
        page,
        projectId,
        parentId,
        `subtask-${i}-${Date.now()}`
      );
      subtaskIds.push(subId);
    }

    // Navigate to the board view
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    // Wait for the parent card to appear
    const parentCard = page.locator(`.card[data-task-id="${parentId}"]`);
    await expect(parentCard).toBeVisible({ timeout: 5000 });

    // The progress bar should be visible on the parent card
    const progressBar = getProgressForCard(page, parentId);
    await expect(progressBar).toBeVisible({ timeout: 5000 });

    // The count text should show "3 subtasks"
    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });

    // The progress fill should be at 0% (all subtasks in backlog, none done)
    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*0%?/);
  });

  test('progress updates when subtask status changes', async ({ page }) => {
    // Ensure we're on the board view
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    // Verify initial state: 0% fill (all 3 subtasks in backlog)
    const progressFill = getProgressFillForCard(page, parentId);
    let fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*0%?/);

    // Move first subtask to "done"
    await moveTaskViaApi(page, projectId, subtaskIds[0], 'done');

    // Wait for the progress fill to update (WebSocket broadcast)
    // 1/3 done ≈ 33.33% — check approximate value since browser precision varies
    await expect(async () => {
      const newFill = await getProgressFillForCard(page, parentId).getAttribute('style');
      expect(newFill).toMatch(/width:\s*33\.33/);
    }).toPass({ timeout: 5000 });

    // Move second subtask to "done"
    await moveTaskViaApi(page, projectId, subtaskIds[1], 'done');

    // Wait for the progress fill to update to 2/3
    // 2/3 done ≈ 66.67% — check approximate value
    await expect(async () => {
      const newFill = await getProgressFillForCard(page, parentId).getAttribute('style');
      expect(newFill).toMatch(/width:\s*66\.66/);
    }).toPass({ timeout: 5000 });
  });

  test('no progress indicator for tasks without subtasks', async ({ page }) => {
    // Create a standalone task with no subtasks
    const standaloneTitle = `standalone-${Date.now()}`;
    const standaloneId = await createTaskViaApi(page, projectId, standaloneTitle);

    // Navigate to the board view
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    // Wait for the standalone card to appear
    const standaloneCard = page.locator(`.card[data-task-id="${standaloneId}"]`);
    await expect(standaloneCard).toBeVisible({ timeout: 5000 });

    // The standalone card should NOT have a progress bar
    const progressBar = getProgressForCard(page, standaloneId);
    await expect(progressBar).not.toBeVisible();
  });

  test('progress count reflects total subtask count regardless of statuses', async ({ page }) => {
    // Navigate to the board view
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    // The parent card should show "3 subtasks" — the count reflects the
    // total number of subtasks, not just those in a particular status.
    // (2 subtasks were moved to done in the previous test, 1 remains in backlog)
    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });

    // The progress bar should still be visible
    const progressBar = getProgressForCard(page, parentId);
    await expect(progressBar).toBeVisible({ timeout: 5000 });

    // The fill should be non-zero (at least 1 subtask was moved to done)
    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).not.toMatch(/width:\s*0%?/);
  });

  test('progress shows 100% when all subtasks are done', async ({ page }) => {
    // Navigate to the board view
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    // Move ALL subtasks to "done" to ensure 100% regardless of prior state
    for (const subId of subtaskIds) {
      await moveTaskViaApi(page, projectId, subId, 'done');
    }

    // Reload the board to ensure server state is reflected
    await page.reload();
    await waitForBoard(page);

    // The progress fill should be at 100% (all 3 subtasks done)
    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*100%?/);

    // The count text should still show "3 subtasks"
    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });
  });
});
