import { test, expect, Page } from '@playwright/test';
import { getProjectId } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Subtask Auto-Start', () => {
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
        auto_commit: false, // Use review flow so we can test sibling auto-start on review
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

  // Helper: get task data from API
  async function getTask(page: Page, taskId: string): Promise<Record<string, any>> {
    return await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
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
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    // Both subtasks should be moved to in_progress
    await waitForTaskStatus(page, sub1Id, 'in_progress');
    await waitForTaskStatus(page, sub2Id, 'in_progress');

    // Parent should also be in_progress
    await waitForTaskStatus(page, parentId, 'in_progress');

    // One subtask should be running (the executor started), the other idle
    sub1 = await getTask(page, sub1Id);
    sub2 = await getTask(page, sub2Id);
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
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    // Parent should be in_progress
    await waitForTaskStatus(page, parentId, 'in_progress');

    // All descendants (children AND grandchildren) should be moved to in_progress
    await waitForTaskStatus(page, subBId, 'in_progress');
    await waitForTaskStatus(page, subCId, 'in_progress');
    await waitForTaskStatus(page, subDId, 'in_progress');
  });

  test('subtask moved to review auto-starts next sibling', async ({ page }) => {
    // Create parent with two subtasks
    const parentId = await createTask(page, `parent-review-${Date.now()}`);
    const sub1Id = await createTask(page, `review-sub1-${Date.now()}`, parentId);
    const sub2Id = await createTask(page, `review-sub2-${Date.now()}`, parentId);

    // Start parent (starts first subtask)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    // Wait for first subtask to be in_progress
    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move first subtask to review
    await moveTask(page, sub1Id, 'review');

    // Second subtask should auto-start
    await waitForTaskStatus(page, sub2Id, 'in_progress');
    await waitForTaskState(page, sub2Id, 'running', 15000);
  });

  test('subtask moved to done auto-starts next sibling', async ({ page }) => {
    // Create parent with two subtasks
    const parentId = await createTask(page, `parent-done-${Date.now()}`);
    const sub1Id = await createTask(page, `done-sub1-${Date.now()}`, parentId);
    const sub2Id = await createTask(page, `done-sub2-${Date.now()}`, parentId);

    // Start parent
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move first subtask to done
    await moveTask(page, sub1Id, 'done');

    // Second subtask should auto-start
    await waitForTaskStatus(page, sub2Id, 'in_progress');
    await waitForTaskState(page, sub2Id, 'running', 15000);
  });

  test('subtask natural review transition auto-starts next sibling', async ({ page }) => {
    // When the LLM finishes without calling task_complete (natural completion),
    // the subtask transitions to review and the next sibling should auto-start.
    // Use a prompt that avoids tool-related keywords so mock returns text-only response.
    const parentId = await createTask(page, `parent-natural-${Date.now()}`);
    const sub1Id = await createTask(page, `add error handling to the module`, parentId);
    const sub2Id = await createTask(page, `implement caching layer`, parentId);

    // Start parent (starts first subtask)
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    // Wait for a subtask to start running
    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Wait for the first subtask to transition to review (LLM finishes, no tool_complete)
    // The mock LLM returns text-only for these prompts, triggering natural completion
    await waitForTaskStatus(page, sub1Id, 'review', 30000);

    // The remaining sibling should auto-start.
    // With the fast mock LLM, sub2 may complete (review) before we check,
    // so we verify it left backlog — it should be in_progress, running, or already review.
    await expect(async () => {
      const sub2 = await getTask(page, sub2Id);
      expect(sub2.status).not.toBe('backlog');
    }).toPass({ timeout: 15000, intervals: [200] });
  });

  test('parent is not auto-marked done when all subtasks complete', async ({ page }) => {
    // Create parent with one subtask
    const parentId = await createTask(page, `parent-no-auto-${Date.now()}`);
    const sub1Id = await createTask(page, `no-auto-sub1-${Date.now()}`, parentId);

    // Start parent
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
    }, { pid: projectId, tid: parentId });

    await waitForTaskStatus(page, sub1Id, 'in_progress');

    // Move subtask to done
    await moveTask(page, sub1Id, 'done');

    // Parent should NOT be auto-marked as done
    const parent = await getTask(page, parentId);
    expect(parent.status).not.toBe('done');
    // Parent should still be in_progress
    expect(parent.status).toBe('in_progress');
  });
});
