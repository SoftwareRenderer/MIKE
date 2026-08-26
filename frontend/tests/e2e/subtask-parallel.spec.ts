import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  csrfFetch,
  getTaskData,
  apiGetConfig,
  apiPutConfig,
  waitForConfig,
  findProviderByRef,
} from './helpers';

// ============================================================
// SUBTASK PARALLEL — parent P → G1, G2, G3 (three leaf subtasks)
//
// With "run siblings in parallel" enabled and the provider concurrency high,
// starting the parent P kicks off ALL three subtasks at once (the start
// handler fans out synchronously) instead of the sequential single-start +
// autostart chain. We poll and assert that at some moment 2+ subtasks are
// in-flight (in_progress) simultaneously — proof of the parallel fan-out.
//
// The prompts route the mock LLM to a slow turn (slow-start → 3s initial
// delay + slow streaming), so in-flight subtasks stay running long enough to
// observe them concurrently.
//
// Runs in the `isolated` project (port 3001): it flips a config toggle and
// the mock provider's concurrency (both are config modifications).
// ============================================================

/** Set the mock provider's concurrency and wait for it to persist. */
async function setMockConcurrency(page: Page, concurrency: number) {
  const cfg = await apiGetConfig(page);
  const providers = (cfg?.providers || []).map((p: any) =>
    (p.url || '').includes(':9999') ? { ...p, concurrency } : p
  );
  await apiPutConfig(page, { ...cfg, providers });
  await waitForConfig(
    page,
    (c: any) =>
      (c?.providers || []).some(
        (p: any) => (p.url || '').includes(':9999') && p.concurrency === concurrency
      ),
    5000
  );
}

/** Enable/disable the "run siblings in parallel" toggle and wait for it to persist. */
async function setRunSiblingsParallel(page: Page, on: boolean) {
  const cfg = await apiGetConfig(page);
  await apiPutConfig(page, { ...cfg, run_siblings_parallel: on });
  await waitForConfig(page, (c: any) => (c?.run_siblings_parallel === true) === on, 5000);
}

/** Start a task via the API (real start path → executor). */
async function startTask(page: Page, projectId: string, taskId: string): Promise<void> {
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
    method: 'POST',
  });
  if (!result.ok) {
    throw new Error(`startTask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
}

/** Create a leaf subtask under a parent (LLM config from the worker provider). */
async function createSubtask(page: Page, projectId: string, parentTaskId: string, prompt: string): Promise<string> {
  const config = await apiGetConfig(page);
  const worker = findProviderByRef(config?.providers || [], config?.worker_provider);
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
    method: 'POST',
    body: {
      prompt,
      parent_task_id: parentTaskId,
      llama_server_url: worker?.url || '',
      model_name: config?.worker_model || '',
    },
  });
  if (!result.ok) {
    throw new Error(`createSubtask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
  return result.data?.id as string;
}

test.describe.serial('subtask parallel start', () => {
  let projectId: string;
  let parentId: string;
  const children: string[] = [];
  const ts = Date.now();

  test.beforeEach(async ({ page }) => {
    children.length = 0;
    parentId = '';
    // High concurrency so all three subtasks can actually run together.
    await setMockConcurrency(page, 4);
    await setRunSiblingsParallel(page, true);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    // Parent with three leaf subtasks. Slow prompts keep each turn in-flight
    // long enough to observe the three subtasks running concurrently.
    parentId = await createTaskViaApi(page, projectId, `parallel parent task ${ts} alpha`);
    for (let i = 1; i <= 3; i++) {
      children.push(await createSubtask(page, projectId, parentId, `parallel child ${i} slow-start stop-test ${ts} beta`));
    }
  });

  test.afterEach(async ({ page }) => {
    // Reset config so other isolated tests are unaffected.
    await setRunSiblingsParallel(page, false);
    await setMockConcurrency(page, 1);
  });

  test('starting the parent starts all three subtasks at once (2+ in-flight simultaneously)', async ({ page }) => {
    test.setTimeout(120000);

    await startTask(page, projectId, parentId);

    // Sample subtask states rapidly. In parallel mode the start handler fans
    // out synchronously, so all three reach in_progress together and we
    // observe 2+ in-flight at the same time. Sequential mode (one at a time)
    // would only ever show a single in-flight subtask.
    let maxInFlight = 0;
    let allLeftBacklog = false;
    const deadline = Date.now() + 12000;
    while (Date.now() < deadline) {
      const states = await Promise.all(children.map((cid) => getTaskData(page, projectId, cid)));
      const inFlight = states.filter((t) => t && t.status === 'in_progress').length;
      if (inFlight > maxInFlight) maxInFlight = inFlight;
      if (states.length > 0 && states.every((t) => t && t.status !== 'backlog')) allLeftBacklog = true;
      if (maxInFlight >= 2 && allLeftBacklog) break;
      await new Promise((r) => setTimeout(r, 150));
    }

    expect(allLeftBacklog, 'all three subtasks should leave the backlog column after the parent starts').toBe(true);
    expect(
      maxInFlight,
      `expected 2+ subtasks in-flight simultaneously (parallel fan-out), observed max ${maxInFlight}`
    ).toBeGreaterThanOrEqual(2);
  });
});
