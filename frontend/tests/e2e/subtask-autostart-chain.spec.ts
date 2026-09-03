import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, getTaskData, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


// ============================================================
// SUBTASK AUTOSTART CHAIN — P → C1 → G1, G2
//
// Starting the parent P (POST /start) must:
//  - move the whole chain to In Progress (started-but-pending),
//  - start the lowest-level subtask (G1) first,
//  - continue the chain: G2 after G1's turn; C1 only after BOTH
//    G1 and G2 are Done; P (the root) runs last, after C1 is Done.
//
// Auto-started tasks run against the mock LLM (text response →
// Review), which is exactly why the chain only advances when we
// explicitly move tasks to Done — Review is a human gate.
// ============================================================

test.describe.serial('subtask autostart chain', () => {
  let projectId: string;
  let parentId: string;
  let c1Id: string;
  let g1Id: string;
  let g2Id: string;
  const suffix = Date.now();

  /** Create a subtask under the given parent (model config from worker). */
  async function createSubtask(page: Page, parentTaskId: string, prompt: string): Promise<string> {
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    const provider = config.providers?.find((p: any) => p.name === config.worker_provider);
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt,
        parent_task_id: parentTaskId,
        llama_server_url: provider?.url || '',
        model_name: config?.worker_model || '',
      },
    });
    if (!result.ok) {
      throw new Error(`createSubtask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
    }
    return result.data?.id;
  }

  async function moveTask(page: Page, taskId: string, status: string): Promise<void> {
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status },
    });
    if (!result.ok) {
      throw new Error(`moveTask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
    }
  }

  /**
   * Poll until the task has actually started an execution: running now, or
   * already finished its (fast mock-LLM) turn and sitting in Review.
   * A task that never starts stays in_progress/idle and the poll times out.
   */
  async function expectTaskStarted(page: Page, taskId: string, label: string): Promise<void> {
    await expect
      .poll(
        async () => {
          const task = await getTaskData(page, projectId, taskId);
          return task.execution_state === 'running' || task.status === 'review';
        },
        { timeout: 15000, message: `${label} should have been auto-started` }
      )
      .toBe(true);
  }

  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({
      storageState: '/tmp/kanban-test-storage-state.json',
    });
    const page = await context.newPage();
    await gotoProject(page);
    projectId = await getProjectId(page);

    // Build the chain P → C1 → G1, G2 (all in Backlog).
    parentId = await createTaskViaApi(page, projectId, `chain parent ${suffix}`);
    c1Id = await createSubtask(page, parentId, `chain c1 ${suffix}`);
    g1Id = await createSubtask(page, c1Id, `chain g1 ${suffix}`);
    g2Id = await createSubtask(page, c1Id, `chain g2 ${suffix}`);

    // Start the parent.
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${parentId}/start`, {
      method: 'POST',
    });
    if (!result.ok) {
      throw new Error(`start parent: API returned ${result.status}: ${JSON.stringify(result.data)}`);
    }
    await context.close();
  });

  test.afterAll(async ({ browser }) => {
    // Best-effort cleanup: stop the chain and delete the root (descendants
    // are deleted recursively).
    try {
      const context = await browser.newContext({
        storageState: '/tmp/kanban-test-storage-state.json',
      });
      const page = await context.newPage();
      if (parentId) {
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${parentId}/stop`, { method: 'POST' });
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${parentId}`, { method: 'DELETE' });
      }
      await context.close();
    } catch {
      // best effort
    }
  });

  test('starting the parent moves the chain to In Progress and starts the lowest-level subtask first', async ({ page }) => {
    await gotoProject(page);
    const parent = await getTaskData(page, projectId, parentId);
    expect(parent.status, 'parent should be In Progress (started-but-pending)').toBe('in_progress');
    expect(parent.execution_state, 'parent should stay pending while subtasks run').toBe('idle');

    const c1 = await getTaskData(page, projectId, c1Id);
    expect(c1.status, 'intermediate subtask should be In Progress (started-but-pending)').toBe('in_progress');
    expect(c1.execution_state, 'intermediate subtask should stay pending').toBe('idle');

    // The lowest-level subtask (G1) is the one that starts first.
    await expectTaskStarted(page, g1Id, 'lowest-level subtask G1');

    // P and C1 must still be pending (they cannot run before their children are done).
    const parentAfter = await getTaskData(page, projectId, parentId);
    expect(parentAfter.execution_state).not.toBe('running');
    const c1After = await getTaskData(page, projectId, c1Id);
    expect(c1After.execution_state).not.toBe('running');
  });

  test('the chain continues to the next sibling after the first leaf finishes its turn', async ({ page }) => {
    await gotoProject(page);
    // G1's fast mock-LLM turn ends in Review, which triggers the chain to
    // auto-start the next leaf (G2).
    await expectTaskStarted(page, g2Id, 'sibling G2');
  });

  test('the intermediate task starts only after ALL its children are Done, and the root runs last', async ({ page }) => {
    await gotoProject(page);
    // Both leaves in Review are NOT done — C1 must not start yet.
    const c1Before = await getTaskData(page, projectId, c1Id);
    expect(c1Before.execution_state, 'C1 must wait until both leaves are Done').not.toBe('running');

    await moveTask(page, g1Id, 'done');
    await moveTask(page, g2Id, 'done');

    // All of C1's children are now Done → C1 auto-starts.
    await expectTaskStarted(page, c1Id, 'intermediate task C1');

    // Root P must not start while C1 is not done.
    const parentBefore = await getTaskData(page, projectId, parentId);
    expect(parentBefore.execution_state, 'root must wait for C1').not.toBe('running');

    await moveTask(page, c1Id, 'done');

    // All of P's children are now Done → P (the root) starts last.
    await expectTaskStarted(page, parentId, 'root task P');
  });
});
