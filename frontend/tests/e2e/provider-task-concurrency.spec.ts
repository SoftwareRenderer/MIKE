import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  navigateToTask,
  getTaskData,
  apiGetConfig,
  apiPutConfig,
  waitForConfig,
  csrfFetch,
} from './helpers';

// Provider task concurrency: the per-provider `concurrency` setting caps the
// number of RUNNING TASKS on a provider. With concurrency 1, a second task on
// the same provider starts (in_progress) but waits for a slot — "Pending"
// badge (execution_state idle) — with a queue indicator in its chat, instead
// of issuing an LLM call. When the first task goes idle (turn complete), the
// slot hands off to the next queued task. A user message to an idle task
// re-acquires a slot (queueing again if the provider is still saturated).
// Utility calls (title generation) keep going through the per-request gate:
// they queue behind an in-flight stream and must not deadlock.
//
// Runs in the `isolated` project (port 3001): it changes the mock provider's
// concurrency, which is a config modification.
//
// Timing: task prompts route the mock LLM to `text_response_slow` (16 chunks
// × 100ms) + the `slow-start` keyword (3s initial delay) → each turn lasts
// ~5s (a text-only "stop" response triggers the loop detector's hint, then a
// fast follow-up response ends the turn). The follow-up message uses a plain
// prompt → fast text response. Prompts are 6+ words so title generation runs
// (≤5-word first lines are used as titles verbatim, skipping the LLM call).

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

/** Start a task via the API (real start path → executor). */
async function startTask(page: Page, projectId: string, taskId: string): Promise<void> {
  const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, {
    method: 'POST',
  });
  if (!result.ok) {
    throw new Error(`startTask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
  }
}

/** Poll the task API until the predicate matches (or timeout). */
async function waitTaskState(
  page: Page,
  projectId: string,
  taskId: string,
  match: (t: any) => boolean,
  timeout = 20000
): Promise<void> {
  await expect(async () => {
    const t = await getTaskData(page, projectId, taskId);
    expect(
      match(t),
      `task ${taskId} state: status=${t?.status} execution_state=${t?.execution_state}`
    ).toBe(true);
  }).toPass({ timeout });
}

/** Number of assistant messages persisted for a task. */
async function countAssistantMessages(page: Page, projectId: string, taskId: string): Promise<number> {
  const msgs = await page.evaluate(async ({ pid, tid }) => {
    const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
    return res.ok ? res.json() : [];
  }, { pid: projectId, tid: taskId });
  return (msgs || []).filter((m: any) => m.role === 'assistant').length;
}

/**
 * Send a message via the chat UI and verify it was persisted server-side.
 * (The shared sendMessage helper counts user messages in the DOM, which is
 * racy here: the task chat already contains a user-role loop-hint message,
 * and the optimistic echo can render before the pre-send count is taken.)
 */
async function sendChatMessage(page: Page, message: string): Promise<void> {
  await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });
  const chatTextarea = page.locator('textarea[placeholder*="message"]');
  await chatTextarea.fill(message);
  await chatTextarea.press('Enter');
}

test.describe.serial('Provider Task Concurrency', () => {
  let projectId: string;

  // page is per-test — use per-test hooks (single test here).
  test.beforeEach(async ({ page }) => {
    await setMockConcurrency(page, 1);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test.afterEach(async ({ page }) => {
    await setMockConcurrency(page, 16);
  });

  test('queues tasks beyond the limit, hands off slots, re-acquires on resume', async ({ page }) => {
    test.setTimeout(120000);
    const ts = Date.now();

    // ── Task 1 acquires the only slot and runs ──────────────────────────
    const prompt1 = `concurrency slow-start stop-test one alpha ${ts}`;
    const task1 = await createTaskViaApi(page, projectId, prompt1);
    await startTask(page, projectId, task1);
    await waitTaskState(page, projectId, task1,
      (t) => t.status === 'in_progress' && t.execution_state === 'running');
    await expect(page.locator(`.card[data-task-id="${task1}"] .badge`, { hasText: 'Running' }))
      .toBeVisible({ timeout: 10000 });

    // ── Tasks 2 and 3 queue as Pending (no LLM call while waiting) ─────
    // Created while task 1's stream is in flight, so their title
    // (utility) calls also queue on the per-request gate behind it.
    const prompt2 = `concurrency slow-start stop-test two beta ${ts}`;
    const prompt3 = `concurrency slow-start stop-test three gamma ${ts}`;
    const task2 = await createTaskViaApi(page, projectId, prompt2);
    const task3 = await createTaskViaApi(page, projectId, prompt3);
    await startTask(page, projectId, task2);
    await startTask(page, projectId, task3);
    await waitTaskState(page, projectId, task2,
      (t) => t.status === 'in_progress' && t.execution_state === 'idle');
    await waitTaskState(page, projectId, task3,
      (t) => t.status === 'in_progress' && t.execution_state === 'idle');
    await expect(page.locator(`.card[data-task-id="${task2}"] .badge`, { hasText: 'Pending' }))
      .toBeVisible({ timeout: 10000 });
    await expect(page.locator(`.card[data-task-id="${task3}"] .badge`, { hasText: 'Pending' }))
      .toBeVisible({ timeout: 10000 });

    // A queued task must not have issued its LLM call yet.
    expect(await countAssistantMessages(page, projectId, task2)).toBe(0);
    expect(await countAssistantMessages(page, projectId, task3)).toBe(0);

    // ── Queue indicator in task 2's chat (task-slot queue) ──────────────
    await navigateToTask(page, task2);
    await expect(page.locator('.awaiting-llm')).toContainText(/position \d+ in queue/, { timeout: 15000 });

    // ── Task 1's turn completes → slot hands off to task 2 ─────────────
    await waitTaskState(page, projectId, task1,
      (t) => t.status === 'review' && t.execution_state === 'idle', 30000);
    await waitTaskState(page, projectId, task2,
      (t) => t.status === 'in_progress' && t.execution_state === 'running', 15000);
    await expect(page.locator(`.card[data-task-id="${task2}"] .badge`, { hasText: 'Running' }))
      .toBeVisible({ timeout: 10000 });

    // Task 3 is still queued behind task 2 (task 2 holds the slot ~5s).
    const t3 = await getTaskData(page, projectId, task3);
    expect(t3.status).toBe('in_progress');
    expect(t3.execution_state).toBe('idle');

    // ── User message to idle task 1 → re-acquires a slot ───────────────
    // Task 2 still holds the slot, so task 1 queues (Pending) first, then
    // runs its (fast) turn once task 2 goes idle.
    const followUp = `concurrency follow-up ${ts}`;
    await navigateToTask(page, task1);
    await sendChatMessage(page, followUp);
    await expect(async () => {
      const msgs = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
        return res.ok ? res.json() : [];
      }, { pid: projectId, tid: task1 });
      expect((msgs || []).some((m: any) => m.role === 'user' && m.content === followUp)).toBe(true);
    }).toPass({ timeout: 15000 });
    await waitTaskState(page, projectId, task1,
      (t) => t.status === 'in_progress', 20000);

    // Task 3 eventually gets the slot after task 2, completes its turn.
    await waitTaskState(page, projectId, task3,
      (t) => t.status === 'review' && t.execution_state === 'idle', 60000);

    // Task 1's follow-up turn completes once it is granted the slot.
    await waitTaskState(page, projectId, task1,
      (t) => t.status === 'review' && t.execution_state === 'idle', 30000);

    // ── Utility calls (title generation) were not deadlocked ────────────
    for (const [tid, prompt] of [[task1, prompt1], [task2, prompt2], [task3, prompt3]] as const) {
      const t = await getTaskData(page, projectId, tid);
      expect(t.title, `task ${tid} title should be generated`).toBeTruthy();
      expect(t.title, `task ${tid} title should differ from its prompt`).not.toBe(prompt);
    }
  });
});
