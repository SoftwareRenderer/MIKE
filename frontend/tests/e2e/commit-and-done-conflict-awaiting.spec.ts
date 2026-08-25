import { test, expect, type Browser } from '@playwright/test';
import { writeFileSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch, createTaskViaApi, navigateToTask, getWorktreePath, worktreeExists } from './helpers';

// ============================================================
// Awaiting LLM indicator after commit-and-done merge conflict
//
// Regression: "Commit and move to Done" that hits a merge conflict
// persists the "A merge conflict occurred when…" user message and
// restarts the LLM via handleLazyStart. That path previously sent no
// status:running event and the client never armed
// stream.state.awaitingResponse — the indicator only appeared after
// a page refresh (onMount arming). Now:
//   - TaskChat.handleCommitAndDone arms it optimistically when the
//     returned task is in_progress, and
//   - the backend emits status:running from handleLazyStart.
//
// Isolation: the conflict leaves an in-progress merge in the main
// checkout, so this spec runs against a DEDICATED repo + project
// (/tmp/kanban-test-repo-cdn-conflict) — never the shared
// kanban-test-repo — and deletes both in afterAll.
//
// Determinism: the task prompt contains "slow-start", which the mock
// LLM expands into a 3s initial delay — the conflict prompt embeds
// the task title/description, so the resolution request is delayed
// too, giving a stable window to observe the indicator.
// ============================================================

const CONFLICT_REPO = '/tmp/kanban-test-repo-cdn-conflict';
const PROJECT_NAME = 'kanban-test-repo-cdn-conflict';

// `page` fixtures aren't available in beforeAll/afterAll — create a
// dedicated context (with the harness's logged-in storage state).
async function setupPage(browser: Browser) {
  const context = await browser.newContext({
    storageState: '/tmp/kanban-test-storage-state.json',
  });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

test.describe.serial('Commit and move to Done (merge conflict) shows Awaiting LLM', () => {
  let projectId = '';

  test.beforeAll(async ({ browser }) => {
    // Fresh dedicated repo (independent of the shared kanban-test-repo).
    // Local git identity is REQUIRED: backend commit-and-done commits
    // don't pass -c author flags.
    rmSync(CONFLICT_REPO, { recursive: true, force: true });
    mkdirSync(CONFLICT_REPO, { recursive: true });
    writeFileSync(join(CONFLICT_REPO, 'README.md'), 'commit-and-done conflict fixture repo\n');
    execSync('git init', { cwd: CONFLICT_REPO, stdio: 'ignore' });
    execSync('git config user.email "test@kanban.local"', { cwd: CONFLICT_REPO, stdio: 'ignore' });
    execSync('git config user.name "Test"', { cwd: CONFLICT_REPO, stdio: 'ignore' });
    execSync('git add README.md', { cwd: CONFLICT_REPO, stdio: 'ignore' });
    execSync('git commit -m "Initial commit"', { cwd: CONFLICT_REPO, stdio: 'ignore' });

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', {
        method: 'POST',
        body: { path: CONFLICT_REPO, name: PROJECT_NAME },
      });
      if (!res.ok) throw new Error(`create conflict project: ${JSON.stringify(res.data)}`);
      projectId = res.data.id;
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) {
        await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
      }
    } finally {
      await context.close();
    }
    rmSync(CONFLICT_REPO, { recursive: true, force: true });
  });

  test('conflict prompt shows the awaiting indicator without a reload', async ({ page }) => {
    test.setTimeout(120000);
    const unique = Date.now();
    const file = `conflict-${unique}.txt`;

    // Base version committed on the default branch BEFORE the task branch
    // diverges, so the later main/task edits genuinely conflict.
    writeFileSync(join(CONFLICT_REPO, file), 'base version\n');
    execSync(`git add ${file} && git commit -m "seed base version"`, { cwd: CONFLICT_REPO, stdio: 'ignore' });

    // "slow-start" in the prompt → mock LLM delays every response for this
    // conversation by 3s (the conflict prompt embeds the title/description).
    const taskId = await createTaskViaApi(page, projectId, `slow-start cdn-conflict-${unique}`);

    // Start the task so its worktree (and branch) is created.
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    expect(moveResult.ok).toBe(true);

    // Poll for the worktree (graceful skip if the environment can't create one).
    let worktreeReady = false;
    for (let i = 0; i < 40; i++) {
      await new Promise(r => setTimeout(r, 500));
      if (worktreeExists(projectId, taskId)) {
        worktreeReady = true;
        break;
      }
    }
    if (!worktreeReady) {
      test.skip();
    }

    // Task side: diverging edit, staged (committed by commit-and-done).
    const wt = getWorktreePath(projectId, taskId);
    writeFileSync(join(wt, file), 'task version\n');
    execSync(`git -C ${wt} add ${file}`);

    // Main side: conflicting edit committed directly on the branch.
    writeFileSync(join(CONFLICT_REPO, file), 'main version\n');
    execSync(`git -C ${CONFLICT_REPO} add ${file} && git -C ${CONFLICT_REPO} commit -m "main version"`, {
      stdio: 'ignore',
    });

    // Wait for the initial (slow-start delayed) LLM turn to persist an
    // assistant reply — otherwise the last chat entry is the user prompt
    // and the review bar shows "Resume" instead of "Commit and move to Done".
    await expect(async () => {
      const msgs = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
        return res.ok ? await res.json() : [];
      }, { pid: projectId, tid: taskId });
      expect(Array.isArray(msgs) && msgs.some((m: any) => m.role === 'assistant')).toBe(true);
    }).toPass({ timeout: 30000 });

    // Move to review (stops the initial executor so nothing else touches the worktree).
    const reviewResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(reviewResult.ok).toBe(true);

    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // The commit button only renders once diff stats report staged changes.
    const commitBtn = page.locator('.btn-commit-and-done');
    await expect(commitBtn).toBeVisible({ timeout: 15000 });

    // No reload at this point — everything after must update live.
    await commitBtn.click();

    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 15000 });
    await textarea.fill(`cdn conflict test commit ${unique}`); // also cancels streaming
    const confirmBtn = commitDialog.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 15000 });
    await confirmBtn.click();

    // The merge conflicts — the persisted conflict prompt must appear in chat.
    const conflictMsg = page.locator('.chat-message', { hasText: 'A merge conflict occurred' });
    await expect(conflictMsg).toBeVisible({ timeout: 30000 });

    // REGRESSION: the "Awaiting LLM" indicator must show automatically —
    // no page refresh. (Before the fix it only appeared after reload.)
    const indicator = page.locator('.awaiting-llm');
    await expect(indicator).toBeVisible({ timeout: 15000 });
    await expect(indicator).toContainText('Awaiting');

    // Hides once the (slow-start delayed) mock response streams its first event.
    await expect(indicator).toBeHidden({ timeout: 30000 });
  });
});
