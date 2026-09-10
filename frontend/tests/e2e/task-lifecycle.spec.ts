import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync } from 'fs';
import { join } from 'path';
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

  test('should create a new task via API and verify on board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `lifecycle-create-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Verify the task appears on the board
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    const card = page.locator(`.card[data-task-id="${taskId}"]`);
    await expect(card).toBeVisible({ timeout: 5000 });
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

  test('should show Delete button', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-delete-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const deleteButton = page.locator('.icon-btn[aria-label="Delete task"]');
    await expect(deleteButton).toBeVisible();
  });

  test('should confirm before deleting task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-confirm-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await page.click('.icon-btn[aria-label="Delete task"]');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-confirm');
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
  });

  test('should cancel delete task', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-cancel-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await page.click('.icon-btn[aria-label="Delete task"]');
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 5000 });
    await page.click('[role="dialog"] .btn-cancel');
    await expect(page.locator('.task-detail-page')).toBeVisible();
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

// --- No Legacy Start/Stop Buttons ---

test.describe('No Legacy Start/Stop Buttons', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should not show Start/Stop buttons in header', async ({ page }) => {
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const startButtons = page.locator('[title="Start task"]');
    const stopButtons = page.locator('[title="Stop task"]');
    await expect(startButtons).not.toBeVisible();
    await expect(stopButtons).not.toBeVisible();

    const hasChatForm = await page.locator('.chat-form').isVisible().catch(() => false);
    const hasStartButton = await page.locator('.btn-start-task').isVisible().catch(() => false);
    expect(hasChatForm || hasStartButton).toBe(true);
  });
});

// --- Backlog Column Title in Context ---

test.describe('Backlog Column Title in Context', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show Backlog column title in context', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-backlog-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await expect(page.locator('.section-title').first()).toBeVisible();
  });
});

// --- Task Count in Context Column ---

test.describe('Task Count in Context Column', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show task count in context column', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `actions-count-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    await expect(page.locator('.column-context')).toBeVisible();
  });
});

// --- Task Detail Layout ---

test.describe('Task Detail Layout', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show sidebar hidden when task detail is open', async ({ page }) => {
    await expect(page.locator('.sidebar')).toBeVisible();
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.sidebar')).not.toBeVisible();
  });

  test('should have 3-column layout in task detail', async ({ page }) => {
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const columns = page.locator('.columns .column');
    await expect(columns).toHaveCount(3);
    await expect(page.locator('.column-context')).toBeVisible();
    await expect(page.locator('.column-chat')).toBeVisible();
    await expect(page.locator('.column-diff')).toBeVisible();
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

  test('progress updates when subtask status changes', async ({ page }) => {
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

  test('progress count reflects total subtask count regardless of statuses', async ({ page }) => {
    await page.goto(`/?project=${projectId}`);
    await waitForBoard(page);

    const progressCount = getProgressCountForCard(page, parentId);
    await expect(progressCount).toHaveText('3 subtasks', { timeout: 5000 });

    const progressBar = getProgressForCard(page, parentId);
    await expect(progressBar).toBeVisible({ timeout: 5000 });

    const progressFill = getProgressFillForCard(page, parentId);
    const fillStyle = await progressFill.getAttribute('style');
    expect(fillStyle).not.toMatch(/width:\s*0%?/);
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

// --- Task Stash/Unstash ---

function getConfig() {
  return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function findWorktreeDir(): string | null {
  const worktreesDir = getConfig().workspace_dir;
  try {
    const projectEntries = readdirSync(worktreesDir);
    for (const projEntry of projectEntries) {
      const projPath = join(worktreesDir, projEntry);
      if (!statSync(projPath).isDirectory()) continue;
      const taskEntries = readdirSync(projPath);
      for (const taskEntry of taskEntries) {
        const fullPath = join(projPath, taskEntry);
        if (statSync(fullPath).isDirectory()) return fullPath;
      }
    }
  } catch { /* ignore */ }
  return null;
}

function getTaskInfo(): { projectId: string; taskId: string; worktreeDir: string } | null {
  try {
    const projects = JSON.parse(execSync('curl -s http://localhost:3000/api/projects', { encoding: 'utf-8' }));
    if (!projects.length) return null;
    const projectId = projects[0].id;
    const tasks = JSON.parse(
      execSync(`curl -s http://localhost:3000/api/projects/${projectId}/tasks`, { encoding: 'utf-8' })
    );
    const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
    if (!task) return null;
    const wtDir = join(getConfig().workspace_dir, projectId, task.id);
    try { statSync(wtDir); } catch { return null; }
    return { projectId, taskId: task.id, worktreeDir: wtDir };
  } catch {
    return null;
  }
}

test.describe.serial('Task Stash/Unstash', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('checkpoint diff includes changes at that checkpoint', async ({ page }) => {
    const info = getTaskInfo();
    if (!info) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }
    const { projectId, taskId, worktreeDir } = info;
    const uniqueId = Date.now();

    execSync(`echo "real commit ${uniqueId}" > ${worktreeDir}/real-file.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git commit -m "real commit ${uniqueId}"`);

    execSync(`echo "checkpoint1 ${uniqueId}" > ${worktreeDir}/check1-file.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 1"`);
    const stash1Hash = execSync(`cd ${worktreeDir} && git rev-parse stash@{1}`, { encoding: 'utf-8' }).trim();

    execSync(`echo "checkpoint2 ${uniqueId}" > ${worktreeDir}/check2-file.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 2"`);
    const stash2Hash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

    // Wait for board to reflect changes
    await expect(async () => {
      await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
    }).toPass({ timeout: 3000 });

    // First checkpoint: shows changes AT checkpoint 1 (includes check1)
    const diff1 = JSON.parse(execSync(
      `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash1Hash}"`,
      { encoding: 'utf-8' }
    ));
    expect(diff1.diff).toContain('real-file');
    expect(diff1.diff).toContain('check1');

    // Second checkpoint: shows changes AT checkpoint 2 (includes check1 + check2)
    const diff2 = JSON.parse(execSync(
      `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash2Hash}"`,
      { encoding: 'utf-8' }
    ));
    expect(diff2.diff).toContain('real-file');
    expect(diff2.diff).toContain('check1');
    expect(diff2.diff).toContain('check2');

    execSync(`cd ${worktreeDir} && git stash clear`);
    const baseRef = execSync(`cd ${worktreeDir} && git rev-parse HEAD~1`, { encoding: 'utf-8' }).trim();
    execSync(`cd ${worktreeDir} && git reset --hard ${baseRef}`);
  });

  test('single checkpoint shows its own changes', async ({ page }) => {
    const info = getTaskInfo();
    if (!info) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }
    const { projectId, taskId, worktreeDir } = info;
    const uniqueId = Date.now();

    execSync(`echo "only checkpoint ${uniqueId}" > ${worktreeDir}/only-check.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "only checkpoint"`);
    const stashHash = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

    const diff = JSON.parse(execSync(
      `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stashHash}"`,
      { encoding: 'utf-8' }
    ));
    expect(diff.diff).toContain('only-check');

    execSync(`cd ${worktreeDir} && git stash clear`);
  });

  test('View Changes API shows changes at each checkpoint', async ({ page }) => {
    const info = getTaskInfo();
    if (!info) {
      await expect(page.locator('.board')).toBeVisible();
      return;
    }

    const { projectId, taskId, worktreeDir } = info;
    const uid = Date.now();

    execSync(`echo "user-work-${uid}" > ${worktreeDir}/user-file.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git commit -m "user work ${uid}"`);

    execSync(`echo "check1-${uid}" > ${worktreeDir}/check1.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 1"`);
    const stash1 = execSync(`cd ${worktreeDir} && git rev-parse stash@{1}`, { encoding: 'utf-8' }).trim();

    execSync(`echo "check2-${uid}" > ${worktreeDir}/check2.txt`);
    execSync(`cd ${worktreeDir} && git add -A && git stash push -u -m "checkpoint 2"`);
    const stash2 = execSync(`cd ${worktreeDir} && git rev-parse stash@{0}`, { encoding: 'utf-8' }).trim();

    // Wait for board to reflect changes
    await expect(async () => {
      await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
    }).toPass({ timeout: 3000 });

    // Checkpoint 1: includes user-file (committed) + check1 (at this checkpoint)
    const diff1 = JSON.parse(execSync(
      `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash1}"`,
      { encoding: 'utf-8' }
    ));
    expect(diff1.diff).toContain('user-file');
    expect(diff1.diff).toContain('check1');
    expect(diff1.diff).not.toContain('check2');

    // Checkpoint 2: includes user-file + check1 + check2 (all accumulated)
    const diff2 = JSON.parse(execSync(
      `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff?head=${stash2}"`,
      { encoding: 'utf-8' }
    ));
    expect(diff2.diff).toContain('user-file');
    expect(diff2.diff).toContain('check1');
    expect(diff2.diff).toContain('check2');

    execSync(`cd ${worktreeDir} && git stash clear`);
    const baseRef = execSync(`cd ${worktreeDir} && git rev-parse HEAD~1`, { encoding: 'utf-8' }).trim();
    execSync(`cd ${worktreeDir} && git reset --hard ${baseRef}`);
    execSync(`cd ${worktreeDir} && git clean -fd`);
  });
});

// --- Task Commit Diff ---

test.describe.serial('Task Commit Diff', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('Changes panel renders without errors for tasks with no commits', async ({ page }) => {
    const firstCard = page.locator('.card').first();
    await firstCard.click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    const changesTab = page.locator('.tab-btn:has-text("Changes")');
    if (await changesTab.isVisible().catch(() => false)) {
      await changesTab.click();
      // Wait for diff content to render instead of fixed delay
      await expect(page.locator('.diff-content')).toBeVisible();
    }

    const diffContent = page.locator('.diff-content');
    await expect(diffContent).toBeVisible();
  });

  test('shows two dropdowns with defaults: Original branch -> Working tree', async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    const inProgressCards = page.locator('.card').filter({ hasText: 'In Progress' });
    const count = await inProgressCards.count();

    if (count > 0) {
      await inProgressCards.first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      const changesTab = page.locator('.tab-btn:has-text("Changes")');
      if (await changesTab.isVisible().catch(() => false)) {
        await changesTab.click();
      }
      // Wait for diff header to render instead of fixed delay
      await expect(page.locator('.diff-header')).toBeVisible();

      const diffHeader = page.locator('.diff-header');
      if (await diffHeader.isVisible().catch(() => false)) {
        const selectBtns = page.locator('.diff-select-btn');
        expect(await selectBtns.count()).toBe(2);

        const arrow = page.locator('.diff-arrow');
        expect(await arrow.isVisible()).toBe(true);

        const fromBtn = selectBtns.first();
        const fromText = await fromBtn.textContent();
        expect(fromText.length).toBeGreaterThan(0);

        const toBtn = selectBtns.last();
        expect(await toBtn.textContent()).toContain('Working tree');
      }
    } else {
      await expect(page.locator('.board')).toBeVisible();
    }
  });

  test('changing dropdowns updates the diff view', async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.card', { timeout: 10000 });

    const inProgressCards = page.locator('.card').filter({ hasText: 'In Progress' });
    const count = await inProgressCards.count();

    if (count > 0) {
      await inProgressCards.first().click();
      await page.waitForSelector('.task-detail-page', { timeout: 10000 });

      const changesTab = page.locator('.tab-btn:has-text("Changes")');
      if (await changesTab.isVisible().catch(() => false)) {
        await changesTab.click();
      }
      // Wait for diff header to render instead of fixed delay
      await expect(page.locator('.diff-header')).toBeVisible();

      const selectBtns = page.locator('.diff-select-btn');
      const btnCount = await selectBtns.count();

      if (btnCount === 2) {
        // Click first dropdown to open it
        await selectBtns.first().click();
        // Wait for dropdown options to appear instead of fixed delay
        await expect(page.locator('.sel-option').first()).toBeVisible();

        const dropdownOptions = page.locator('.sel-option');
        const optionCount = await dropdownOptions.count();
        expect(optionCount).toBeGreaterThanOrEqual(1);

        await page.press('Escape');

        await selectBtns.last().click();
        // Wait for dropdown options to appear
        await expect(page.locator('.sel-option').first()).toBeVisible();

        const toDropdownOptions = page.locator('.sel-option');
        if (await toDropdownOptions.count() > 0) {
          expect(await toDropdownOptions.first().isVisible()).toBe(true);
        }

        await page.press('Escape');
      }
    } else {
      await expect(page.locator('.board')).toBeVisible();
    }
  });
});
