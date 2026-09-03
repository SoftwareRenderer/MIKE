import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask, getTaskData, worktreeExists, writeFileInWorktree, isolatedProject, gotoProject } from './helpers';

// Each test file gets its own project: see isolatedProject() in helpers.ts.
isolatedProject();


/**
 * Waits until the done-diff REST response is persisted in the app's
 * board-cache IndexedDB store (key `diff:<pid>:<tid>:<headRef>`). An
 * in-flight IDB write would be rolled back by a page navigation and flake
 * the cache-first assertion.
 */
async function waitForDoneDiffPersisted(page: import('@playwright/test').Page, projectId: string, taskId: string): Promise<void> {
  await expect
    .poll(
      () =>
        page.evaluate(
          ({ pid, tid }) =>
            new Promise<boolean>((resolve) => {
              const req = indexedDB.open('kanban-storage');
              req.onsuccess = () => {
                const db = req.result;
                const keysReq = db.transaction('board-cache', 'readonly').objectStore('board-cache').getAllKeys();
                keysReq.onsuccess = () => {
                  db.close();
                  resolve((keysReq.result as string[]).some(k => k.startsWith(`diff:${pid}:${tid}:`)));
                };
                keysReq.onerror = () => resolve(false);
              };
              req.onerror = () => resolve(false);
            }),
          { pid: projectId, tid: taskId },
        ),
      { timeout: 15000 },
    ).toBe(true);
}

/**
 * Watches the app WebSocket for one project. Each connection gets its own observation object, so
 * frames from an older page load cannot contaminate the assertions. `seen` is the latest one:
 *  - `subscribeData`  — the subscribe_project payload (carries known_stats)
 *  - `statFrames`     — task_diff_stats frames for the watched task
 *  - `waitForInitialData()` — resolves on the trailing project_branch frame
 */
function watchProjectFrames(page: import('@playwright/test').Page, projectId: string, taskId: string) {
  type Obs = {
    subscribeData: any;
    statFrames: any[];
    initialDataDone: boolean;
  };
  const freshObs = (): Obs => ({ subscribeData: null, statFrames: [], initialDataDone: false });
  let obs: Obs = freshObs();

  page.on('websocket', ws => {
    // A new connection supersedes any earlier observations (page reload).
    const o = (obs = freshObs());

    ws.on('framesent', e => {
      try {
        const msg = JSON.parse(e.payload.toString());
        if (msg.type === 'subscribe_project' && msg.data?.project_id === projectId) {
          o.subscribeData = msg.data;
        }
      } catch {
        // non-JSON frame — ignore
      }
    });

    ws.on('framereceived', e => {
      try {
        const msg = JSON.parse(e.payload.toString());
        if (msg.type === 'task_diff_stats' && msg.task_id === taskId) {
          o.statFrames.push(msg);
        }
        if (msg.type === 'project_branch') {
          o.initialDataDone = true;
        }
      } catch {
        // non-JSON frame — ignore
      }
    });
  });

  const seen = {
    get subscribeData(): any { return obs.subscribeData; },
    get statFrames(): any[] { return obs.statFrames; },
    get initialDataDone(): boolean { return obs.initialDataDone; },
  };

  return {
    seen,
    async waitForInitialData(timeout = 15000): Promise<void> {
      await expect.poll(() => seen.initialDataDone, { timeout }).toBe(true);
    },
  };
}

test.describe.serial('Done-task diff stats caching', () => {
  let projectId: string;
  let taskId = '';
  let taskTitle = '';

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('done task card keeps diff stats across reloads, and the server skips the re-send', async ({ page }) => {
    // 1. Create + start the task, then add a file that will be committed.
    taskTitle = `done-cache-${Date.now()}`;
    taskId = await createTaskViaApi(page, projectId, taskTitle);
    expect(taskId).not.toBe('');
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    await expect.poll(() => worktreeExists(projectId, taskId), { timeout: 20000 }).toBe(true);
    writeFileInWorktree(projectId, taskId, 'cache-probe.txt', 'line one\nline two\n');
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // 2. Commit and move to Done via the card's action button (deterministic
    // — the chat review bar can show Resume instead of Commit when the task
    // has an unanswered user turn).
    await gotoProject(page);
    const commitAction = page.locator('.card', { hasText: taskTitle }).locator('.action-move[aria-label="Commit and move to done"]');
    await expect(commitAction).toBeVisible({ timeout: 20000 });
    // Hover-revealed action icon under the card title area — force the click.
    await commitAction.click({ force: true });
    await expect(page.locator('[role="dialog"]')).toBeVisible({ timeout: 10000 });
    await page.locator('.commit-textarea').fill('Commit for diff cache test');
    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 15000 });
    await confirmBtn.click();
    await expect
      .poll(async () => (await getTaskData(page, projectId, taskId)).status, { timeout: 20000 })
      .toBe('done');

    // 3. Back to the board — the card shows the diff stats (1 file, +2).
    await gotoProject(page);
    const card = page.locator('.card', { hasText: taskTitle });
    await expect(card).toBeVisible({ timeout: 10000 });
    await expect(card.locator('.diff-files')).toHaveText('1 file', { timeout: 15000 });
    await expect(card.locator('.diff-add')).toHaveText('+2');

    // 4. Full page reload — stats must still render (this reload's stats frame
    // is what fills the client cache for the final check below).
    await page.reload();
    const card2 = page.locator('.card', { hasText: taskTitle });
    await expect(card2).toBeVisible({ timeout: 10000 });
    await expect(card2.locator('.diff-files')).toHaveText('1 file', { timeout: 15000 });
    await expect(card2.locator('.diff-add')).toHaveText('+2');

    // 5. Final reload with frame capture: the subscribe must declare this task
    // in known_stats (client cached the stats from reload #4), and the server
    // must skip the re-send — the initial payload completes without a
    // task_diff_stats frame for this task.
    const { seen, waitForInitialData } = watchProjectFrames(page, projectId, taskId);
    await page.reload();
    await waitForInitialData();
    // Snapshot the count AT the end of the initial payload. The claim being tested is about
    // the initial payload only — a watcher-driven BroadcastDiffStats that lands a moment later
    // (a git event in the worktree, a card re-render) is a different, legitimate frame and
    // must not be counted against it. Reading seen.statFrames later would.
    const initialStatFrames = seen.statFrames.length;
    await expect(page.locator('.card', { hasText: taskTitle }).locator('.diff-files')).toHaveText('1 file', { timeout: 15000 });

    expect(seen.subscribeData, 'subscribe_project with known_stats was not observed').not.toBeNull();
    const known = seen.subscribeData.known_stats || {};
    expect(Object.keys(known)).toContain(taskId);
    const task = await getTaskData(page, projectId, taskId);
    expect(known[taskId]).toBe(task.updated_at);
    // At most one, not zero: a done task still has a branch, so the subscription arms a worktree
    // watcher (app_stream.go) whose first scan sends its own task_diff_stats in this window. The
    // regression is still separable — if known_stats stopped suppressing the snapshot send, the
    // payload would carry both frames.
    expect(initialStatFrames, 'server re-sent the snapshotted stats despite a matching known_stats entry').toBeLessThanOrEqual(1);
  });

  test('done-task detail diff is fetched once, then served from the client cache', async ({ page }) => {
    // The task from the previous test is still done (serial describe).
    const diffRequests: string[] = [];
    page.on('request', req => {
      if (req.url().includes(`/tasks/${taskId}/diff?`)) {
        diffRequests.push(req.url());
      }
    });

    // First open: the canonical done diff (head=branch_tip) is fetched.
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect
      .poll(() => diffRequests.filter(u => u.includes('head=')).length, { timeout: 20000 })
      .toBeGreaterThanOrEqual(1);
    // Wait for the response to actually persist in IndexedDB — an in-flight
    // IDB write would be rolled back by the navigation below.
    await waitForDoneDiffPersisted(page, projectId, taskId);

    // Full page load (board URL — a ?task= URL would restore the detail view
    // and hide the board) and reopen — the cached response (keyed by the head
    // SHA) must serve it with zero canonical-diff network requests.
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 10000 });
    diffRequests.length = 0;
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for the diff panel to render (from cache or network) — a
    // canonical request would have been initiated before this render.
    await expect(page.locator('.diff-file-path', { hasText: 'cache-probe.txt' })).toBeVisible({ timeout: 20000 });
    expect(
      diffRequests.filter(u => u.includes('head=')),
      `expected no done-diff network request after cache, got: ${diffRequests.join(', ')}`,
    ).toHaveLength(0);
  });
});
