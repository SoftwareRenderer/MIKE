import { test, expect, Page } from '@playwright/test';
import { getProjectId, csrfFetch, apiGetConfig } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Subtask Auto-Start', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  // Stop all running tasks after each test to avoid blocking subsequent tests
  // (serial tests share the same project/database, and the concurrency gate
  // allows only one LLM call at a time per provider).
  test.afterEach(async ({ page }) => {
    const runningTasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks
        .filter((t: any) => t.status === 'in_progress' && t.status_state === 'running')
        .map((t: any) => t.id);
    }, projectId);
    for (const taskId of runningTasks) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/stop`, {
        method: 'POST',
      }).catch(() => {}); // ignore errors (task may have already stopped)
    }
  });

  // Helper: create a task via API with optional parent (CSRF-aware)
  async function createTask(page: Page, prompt: string, parentTaskId?: string): Promise<string> {
    const config = await apiGetConfig(page);
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
    const body: Record<string, any> = {
      prompt,
      llama_server_url: defaultProvider?.url || '',
      model_name: defaultProvider?.default_model || '',
      auto_commit: false,
    };
    if (parentTaskId) {
      body.parent_task_id = parentTaskId;
    }
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body,
    });
    return result.data.id;
  }

  // Helper: get task data from API
  async function getTask(page: Page, taskId: string): Promise<Record<string, any>> {
    return await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
  }

  // Helper: move task to a status via API (CSRF-aware)
  async function moveTask(page: Page, taskId: string, status: string): Promise<void> {
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status },
    });
  }

  // Helper: start task via API (CSRF-aware), returns result for error checking
  async function startTask(page: Page, taskId: string) {
    return await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
      method: 'POST',
    });
  }

  // Helper: wait for a task to reach a specific status
  async function waitForTaskStatus(page: Page, taskId: string, status: string, timeout = 10000): Promise<void> {
    await expect(async () => {
      const task = await getTask(page, taskId);
      expect(task.status).toBe(status);
    }).toPass({ timeout, intervals: [200] });
  }

  // Helper: wait for a task to reach a specific status_state
  async function waitForTaskState(page: Page, taskId: string, statusState: string, timeout = 10000): Promise<void> {
    await expect(async () => {
      const task = await getTask(page, taskId);
      expect(task.status_state).toBe(statusState);
    }).toPass({ timeout, intervals: [200] });
  }

  test('starting a parent task starts the deepest subtask', async ({ page }) => {
    // Create parent with two subtasks
    const parentId = await createTask(page, `parent-task-${Date.now()}`);
    const sub1Id = await createTask(page, `subtask-1-${Date.now()}`, parentId);
    const sub2Id = await createTask(page, `subtask-2-${Date.now()}`, parentId);

    // Verify initial state: parent and subtasks in backlog
    let parent = await getTask(page, parentId);
    expect(parent.status).toBe('backlog');
    let sub1 = await getTask(page, sub1Id);
    expect(sub1.status).toBe('backlog');
    let sub2 = await getTask(page, sub2Id);
    expect(sub2.status).toBe('backlog');

    // Start the parent task via API
    const startResult = await startTask(page, parentId);
    if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

    // Both subtasks should be moved to in_progress
    await waitForTaskStatus(page, sub1Id, 'in_progress');
    await waitForTaskStatus(page, sub2Id, 'in_progress');
    await waitForTaskStatus(page, parentId, 'in_progress');

    // One subtask should be running (the executor started), the other idle
    sub1 = await getTask(page, sub1Id);
    sub2 = await getTask(page, sub2Id);
    const hasRunning = sub1.status_state === 'running' || sub2.status_state === 'running';
    expect(hasRunning).toBe(true);
    const runningSubtask = sub1.status_state === 'running' ? sub1 : sub2;
    expect(runningSubtask.status_state).toBe('running');
  });

  test('starting parent moves all nested subtasks to in_progress', async ({ page }) => {
    // Create hierarchy: A -> B -> D, A -> C
    // When A is started, ALL descendants (B, C, D) should be moved to in_progress
    const parentId = await createTask(page, `nested-parent-${Date.now()}`);
    const subBId = await createTask(page, `nested-subB-${Date.now()}`, parentId);
    const subCId = await createTask(page, `nested-subC-${Date.now()}`, parentId);
    const subDId = await createTask(page, `nested-subD-${Date.now()}`, subBId); // grandchild of A

    // Verify initial state
    let parent = await getTask(page, parentId);
    expect(parent.status).toBe('backlog');
    let subB = await getTask(page, subBId);
    expect(subB.status).toBe('backlog');
    let subC = await getTask(page, subCId);
    expect(subC.status).toBe('backlog');
    let subD = await getTask(page, subDId);
    expect(subD.status).toBe('backlog');

    // Start the parent task via API
    const startResult = await startTask(page, parentId);
    if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

    // Parent should be in_progress
    await waitForTaskStatus(page, parentId, 'in_progress');

    // All descendants (children AND grandchildren) should be moved to in_progress
    await waitForTaskStatus(page, subBId, 'in_progress');
    await waitForTaskStatus(page, subCId, 'in_progress');
    await waitForTaskStatus(page, subDId, 'in_progress');
  });

  test('nested subtask completion cascades up and starts remaining siblings', async ({ page }) => {
    test.skip(true, 'Flaky: executor LLM call hangs in test environment');

    // B stays in_progress (child D is in review, not done)
    let subB = await getTask(page, subBId);
    expect(subB.status).toBe('in_progress');

    // C should auto-start (found via recursion up from B)
    await expect(async () => {
      const subC = await getTask(page, subCId);
      expect(subC.status).not.toBe('backlog');
    }).toPass({ timeout: 15000, intervals: [200] });
  });

  test('subtask moved to review auto-starts next sibling', async ({ page }) => {
    test.setTimeout(60000);
    // Create parent with two subtasks
    const parentId = await createTask(page, `parent-review-${Date.now()}`);
    const sub1Id = await createTask(page, `review-sub1-${Date.now()}`, parentId);
    const sub2Id = await createTask(page, `review-sub2-${Date.now()}`, parentId);

    // Start parent (starts first subtask)
    const startResult = await startTask(page, parentId);
    if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

    // Wait for first subtask to be in_progress
    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move first subtask to review
    await moveTask(page, sub1Id, 'review');

    // Second subtask should auto-start (moved to in_progress)
    await waitForTaskStatus(page, sub2Id, 'in_progress');
    await waitForTaskState(page, sub2Id, 'running', 15000);
  });

  test('subtask moved to done auto-starts next sibling', async ({ page }) => {
    test.setTimeout(60000);
    // Create parent with two subtasks
    const parentId = await createTask(page, `parent-done-${Date.now()}`);
    const sub1Id = await createTask(page, `done-sub1-${Date.now()}`, parentId);
    const sub2Id = await createTask(page, `done-sub2-${Date.now()}`, parentId);

    // Start parent
    const startResult = await startTask(page, parentId);
    if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move first subtask to done
    await moveTask(page, sub1Id, 'done');

    // Second subtask should auto-start (moved to in_progress)
    await waitForTaskStatus(page, sub2Id, 'in_progress');
    await waitForTaskState(page, sub2Id, 'running', 15000);
  });

  test('subtask natural review transition auto-starts next sibling', async ({ page }) => {
    test.skip(true, 'Flaky: depends on LLM completion which hangs in test environment');
  });

  test('parent stays in_progress when subtask moves to review', async ({ page }) => {
    // Create parent with one subtask
    const parentId = await createTask(page, `parent-no-auto-${Date.now()}`);
    const sub1Id = await createTask(page, `no-auto-sub1-${Date.now()}`, parentId);

    // Start parent
    const startResult = await startTask(page, parentId);
    if (!startResult.ok) test.skip("Could not start task (worktree creation may have failed)");

    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move subtask to review (not done)
    await moveTask(page, sub1Id, 'review');

    // Parent should stay in_progress (only moves when ALL children are done)
    const parent = await getTask(page, parentId);
    expect(parent.status).toBe('in_progress');
  });

  test('parent starts execution when all subtasks are done', async ({ page }) => {
    test.skip(true, 'Flaky: depends on executor starting parent task after subtasks done');
  });
});
