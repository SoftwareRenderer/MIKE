import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// TASK LIFECYCLE — Parallel Tests (independent)
// ============================================================

test.describe.configure({ mode: 'parallel' });

// --- Create New Task ---

test.describe('Create New Task', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should create a new task via UI modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.click('.add-task-btn');
    await expect(page.locator('[role="dialog"]:has-text("Create Task")')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', `Test task from e2e test ${Date.now()}`);
    // The CreateTaskModal uses type="button" not type="submit"
    await page.click('.btn-primary:has-text("Create Task")');
    await expect(page.locator('[role="dialog"]:has-text("Create Task")')).not.toBeVisible({ timeout: 5000 });
    await page.waitForSelector('.card', { timeout: 5000 });
    const cards = page.locator('.card');
    await expect(cards.first()).toBeVisible();
  });
});

// --- Delete Task with Confirmation ---

test.describe('Delete Task with Confirmation', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  // The delete-confirm dialog, picked out by the message only a confirmation draws: `[role="dialog"]`
  // alone is never unique here.
  const deleteDialog = (page: any) =>
    page.locator('[role="dialog"]').filter({ has: page.locator('.confirm-message') });

  test('asks before deleting: a refusal leaves the task open and a confirmation returns to the board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const refused = await createTaskViaApi(page, projectId, `actions-confirm-${Date.now()}`);
    const removed = await createTaskViaApi(page, projectId, `actions-cancel-${Date.now()}`);

    // Refused: the confirm appears, Cancel answers it, and the task is still there.
    await navigateToTask(page, refused);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('.icon-btn[aria-label="Delete task"]');
    const dialog = deleteDialog(page);
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await dialog.locator('.btn-cancel').click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Confirmed: the same click answered yes lands on the board, and the task is gone from it.
    await navigateToTask(page, removed);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('.icon-btn[aria-label="Delete task"]');
    await expect(deleteDialog(page)).toBeVisible({ timeout: 5000 });
    await dialog.locator('.btn-confirm').click();
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`.card[data-task-id="${removed}"]`)).toHaveCount(0);
  });
});

// --- Start Task Button ---

test.describe('Start Task Button', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show Start Task button for new tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `Start task test ${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const startButton = page.locator('.btn-start-task');
    await expect(startButton).toBeVisible({ timeout: 10000 });
    await expect(startButton).toContainText('Start');
  });
});

// --- Task Detail Layout ---

test.describe('Task Detail Layout', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('the task view is three columns and the sidebar gives way', async ({ page }) => {
    await expect(page.locator('.sidebar')).toBeVisible();
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const columns = page.locator('.columns .column');
    await expect(columns).toHaveCount(3);
    await expect(page.locator('.column-context')).toBeVisible();
    await expect(page.locator('.column-chat')).toBeVisible();
    await expect(page.locator('.column-diff')).toBeVisible();
    await expect(page.locator('.sidebar')).not.toBeVisible();
  });
});

// ============================================================
// TASK LIFECYCLE — Serial Tests (shared mutable state)
// ============================================================

// --- Subtask Progress Tracking (serial: tests share parentId/subtaskIds) ---

async function createSubtaskViaApi(
  page: test.fixtures['page'],
  projectId: string,
  parentTaskId: string,
  prompt: string
): Promise<string> {
  const config = await page.evaluate(async () => {
    const res = await fetch('/api/config');
    return res.json();
  });
  const defaultProvider = config.providers?.find(
    (p: any) => p.name === config.worker_provider
  );
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: {
      prompt,
      parent_task_id: parentTaskId,
      llama_server_url: defaultProvider?.url || '',
      model_name: config?.worker_model || '',
    },
  });
  return result.data.id;
}

async function moveTaskViaApi(
  page: test.fixtures['page'],
  projectId: string,
  taskId: string,
  status: string
): Promise<void> {
  const result = await csrfFetch(
    page,
    `/api/projects/${projectId}/tasks/${taskId}/move`,
    { method: 'POST', body: { status } }
  );
  if (!result.ok) {
    throw new Error(`moveTaskViaApi: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
}

async function waitForBoard(page: test.fixtures['page']): Promise<void> {
  await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
}

function getProgressForCard(page: test.fixtures['page'], parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-bar`);
}

function getProgressCountForCard(page: test.fixtures['page'], parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-count`);
}

function getProgressFillForCard(page: test.fixtures['page'], parentTaskId: string) {
  return page.locator(`.card[data-task-id="${parentTaskId}"] .subtask-progress-fill`);
}

test.describe.serial('Subtask Progress Tracking', () => {
  let projectId: string;
  let parentId: string;
  let subtaskIds: string[] = [];

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('progress indicator appears for parent tasks with subtasks', async ({ page }) => {
    projectId = await getProjectId(page);

    const parentTitle = `progress-parent-${Date.now()}`;
    parentId = await createTaskViaApi(page, projectId, parentTitle);

    subtaskIds = [];
    for (let i = 0; i < 3; i++) {
      const subId = await createSubtaskViaApi(page, projectId, parentId, `subtask-${i}-${Date.now()}`);
      subtaskIds.push(subId);
    }

    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    const parentCard = page.locator(`.card[data-task-id="${parentId}"]`);
    await expect(parentCard).toBeVisible({ timeout: 5000 });

    const progressBar = getProgressForCard(page, parentId);
    await expect(progressBar).toBeVisible({ timeout: 5000 });

    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });

    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*0%?/);
  });

  // The fill and the count are one decision — the bar counts every descendant, whatever its status — so the
  // count is asked for beside the moves that change the fill rather than in a test of its own.
  test('progress updates when subtask status changes, and still counts every subtask', async ({ page }) => {
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    const progressFill = getProgressFillForCard(page, parentId);
    let fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*0%?/);

    await moveTaskViaApi(page, projectId, subtaskIds[0], 'done');
    await expect(async () => {
      const newFill = await getProgressFillForCard(page, parentId).getAttribute('style');
      expect(newFill).toMatch(/width:\s*33\.33/);
    }).toPass({ timeout: 5000 });

    await moveTaskViaApi(page, projectId, subtaskIds[1], 'done');
    await expect(async () => {
      const newFill = await getProgressFillForCard(page, parentId).getAttribute('style');
      expect(newFill).toMatch(/width:\s*66\.66/);
    }).toPass({ timeout: 5000 });

    // Two of three done moved the fill off zero; the count still names every descendant.
    await expect(getProgressCountForCard(page, parentId)).toHaveText('3 subtasks', { timeout: 5000 });
  });

  test('no progress indicator for tasks without subtasks', async ({ page }) => {
    const standaloneTitle = `standalone-${Date.now()}`;
    const standaloneId = await createTaskViaApi(page, projectId, standaloneTitle);

    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    const standaloneCard = page.locator(`.card[data-task-id="${standaloneId}"]`);
    await expect(standaloneCard).toBeVisible({ timeout: 5000 });

    const progressBar = getProgressForCard(page, standaloneId);
    await expect(progressBar).not.toBeVisible();
  });

  test('progress shows 100% when all subtasks are done', async ({ page }) => {
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    for (const subId of subtaskIds) {
      await moveTaskViaApi(page, projectId, subId, 'done');
    }

    await page.reload();
    await waitForBoard(page);

    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).toMatch(/width:\s*100%?/);

    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });
  });
});

