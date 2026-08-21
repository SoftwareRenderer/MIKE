import { test, expect, type Page } from '@playwright/test';
import { existsSync } from 'fs';
import { execSync } from 'child_process';
import { getProjectId, csrfFetch, getTaskData, navigateToTask } from './helpers';

// ============================================================
// PARENT TASK WITH MERGED SUBTASK CHANGES
//
// A parent task whose branch was auto-created for subtask merging (the
// parent never started itself) has no recorded base_ref/base_commit. When
// subtasks merge into its branch, the diff panel must show those changes —
// resolved lazily against the branch's divergence point from main — under
// the "Merged from Subtasks" section, and the action buttons must say
// "Commit and move to done" instead of "Move to done".
//
// Flow (deterministic, no LLM turns needed):
//   1. Parent (backlog, subtasks_auto_merge) + child (parent_task_id).
//   2. Child /init → auto-creates the parent branch + both worktrees.
//   3. Commit a file in the child worktree, then commit-and-done the child
//      (explicit commit message) → merges into the parent branch.
//   4. Parent /diff shows the file (backlog) — then a custom, not-yet
//      existing base branch name is configured on the parent (Edit Task
//      save) and the diff keeps showing the file.
//   5. Move parent to Review → card button says "Commit and move to done"
//      and the task view shows the file under "Merged from Subtasks".
//   6. Commit and move to done (via the card button) → parent done, the
//      custom base branch is created with the file, main is untouched.
// ============================================================

// Shared test repo and worktrees locations (managed by global-setup.ts)
const TEST_REPO = '/tmp/kanban-test-repo';

const worktreePath = (projectId: string, taskId: string) =>
  `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;

function gitIn(dir: string, ...args: string[]): string {
  return execSync(`git -C ${dir} ${args.join(' ')}`, { encoding: 'utf-8' }).trim();
}

test.describe.serial('parent task with merged subtask changes', () => {
  let page: Page;
  let projectId: string;
  let parentId: string;
  let childId: string;
  const suffix = Date.now();
  const fileName = `merged-subtask-${suffix}.txt`;
  const customBase = `explore-idea-${suffix}`;

  test.beforeAll(async ({ browser }) => {
    const context = await browser.newContext({
      storageState: '/tmp/kanban-test-storage-state.json',
    });
    page = await context.newPage();
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test.afterAll(async () => {
    await page?.close();
  });

  test('subtask changes merge into the parent branch and appear in its diff', async () => {
    // Parent in backlog (the only column that accepts subtasks).
    const parentResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: { prompt: `parent with merged subtasks ${suffix}`, subtasks_auto_merge: true },
    });
    expect(parentResult.ok).toBe(true);
    parentId = parentResult.data?.id;
    expect(parentId).not.toBe('');

    // Child attached to the parent (no LLM config — it will never be started).
    const childResult = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: { prompt: `subtask producing merged changes ${suffix}`, parent_task_id: parentId },
    });
    expect(childResult.ok).toBe(true);
    childId = childResult.data?.id;
    expect(childId).not.toBe('');

    // Initializing the child auto-creates the parent branch (parent never
    // starts itself — no base_ref/base_commit recorded) and forks the child
    // worktree from the parent branch. No LLM session is started.
    const initResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${childId}/init`, {
      method: 'POST',
    });
    expect(initResult.ok).toBe(true);

    // Wait for the child worktree to exist.
    await expect
      .poll(
        () => existsSync(worktreePath(projectId, childId)),
        { timeout: 15000, message: 'child worktree should exist after init' }
      )
      .toBe(true);

    // The parent must have a branch but no recorded baseline (the scenario
    // this feature fixes).
    const parentData = await getTaskData(page, projectId, parentId);
    expect(parentData.branch).toBeTruthy();
    expect(parentData.base_ref || '').toBe('');
    expect(parentData.base_commit || '').toBe('');

    // Simulate the subtask's work: a commit on the subtask branch in its
    // worktree.
    const childWt = worktreePath(projectId, childId);
    execSync(
      `cd ${childWt} && echo "merged subtask change" > ${fileName} && ` +
        `git add ${fileName} && ` +
        `git -c user.name=Test -c user.email=test@test.com commit -m "Subtask work"`,
      { encoding: 'utf-8' }
    );

    // Commit-and-done on the subtask (explicit message — no LLM) merges its
    // branch into the parent branch, inside the parent's worktree.
    const commitResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${childId}/commit`, {
      method: 'POST',
      body: { commit_message: 'Subtask complete' },
    });
    expect(commitResult.ok).toBe(true);
    expect(commitResult.data?.status).toBe('done');

    // The child is done (asserted before moving the parent — moving the
    // parent to Review drags descendants along).
    const childData = await getTaskData(page, projectId, childId);
    expect(childData.status).toBe('done');

    // The merged file must now appear in the parent's diff. The parent is
    // still in Backlog with no recorded baseline, so the API resolves
    // lazily against the branch's divergence point from main.
    const diffResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${parentId}/diff`, {
      method: 'GET',
    });
    expect(diffResult.ok).toBe(true);
    expect(diffResult.data?.files?.[fileName]).toBeTruthy();

    // Simulate the user saving Edit Task with a custom base branch name for
    // a separate exploration branch — one that doesn't exist in the repo
    // yet. The diff must keep showing the merged work (an unusable recorded
    // base falls back to the divergence point).
    const updateResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${parentId}`,
      { method: 'PUT', body: { base_ref: customBase } }
    );
    expect(updateResult.ok).toBe(true);
    const diffAfterBase = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${parentId}/diff`,
      { method: 'GET' }
    );
    expect(diffAfterBase.ok).toBe(true);
    expect(diffAfterBase.data?.files?.[fileName]).toBeTruthy();
  });

  test('review parent shows merged changes and "Commit and move to done" buttons', async () => {
    // Move the parent to Review (where the action buttons live).
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${parentId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(moveResult.ok).toBe(true);

    // Open the task view.
    await navigateToTask(page, parentId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The board card's cycle button must offer the commit-and-done action
    // (from the parent's diff stats: committed changes on its branch).
    const cardMoveButton = page.locator('.card.selected button.action-move');
    await expect(cardMoveButton).toHaveAttribute('aria-label', 'Commit and move to done', {
      timeout: 10000,
    });

    // The diff panel shows the merged file under the "Merged from Subtasks"
    // section (the section header button carries .diff-section-merged; the
    // file rows live in the sibling .diff-section-body).
    const mergedSection = page.locator('.diff-section', {
      has: page.locator('.diff-section-merged .diff-section-title'),
    });
    await expect(mergedSection.locator('.diff-section-title')).toHaveText('Merged from Subtasks', {
      timeout: 15000,
    });
    await expect(mergedSection.locator(`.diff-file-path[title="${fileName}"]`)).toBeVisible({
      timeout: 15000,
    });

    // Note: the review bar shows "Resume" here — the parent was never
    // started, so its thread holds only the initial user prompt (intended
    // behavior, covered by resume-awaiting-indicator.spec.ts). The
    // commit-and-done action is offered by the card's cycle button, asserted
    // above.
  });

  test('commit and move to done creates the custom base branch and merges into it', async () => {
    // Click the card's "Commit and move to done" button — opens the commit
    // dialog (streams a generated commit message via the mock LLM).
    await page.locator('.card.selected button.action-move').click();
    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });

    // Fill the commit message (also cancels streaming generation).
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 15000 });
    await textarea.fill(`Parent with merged subtask work ${suffix}`);

    // Wait for the confirm button to be enabled, then confirm.
    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 15000 });
    await confirmBtn.click();

    // Parent moves to Done.
    await expect
      .poll(
        async () => (await getTaskData(page, projectId, parentId)).status,
        { timeout: 20000, message: 'parent should move to Done' }
      )
      .toBe('done');

    // The custom base branch must now exist with the subtask's file — the
    // merge creates a missing configured base before merging into it.
    await expect
      .poll(() => {
        try {
          return gitIn(TEST_REPO, `show ${customBase}:${fileName}`);
        } catch {
          return null;
        }
      }, { timeout: 15000, message: 'file should be on the custom base branch' })
      .toBe('merged subtask change');

    // And the work must NOT have landed on the project's main branch — the
    // point of the custom base is a separate exploration branch. (Use the
    // project's origin branch, not the repo HEAD, to identify "main".)
    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    const mainBranch = project.origin_branch;
    expect(mainBranch).toBeTruthy();
    expect(() => gitIn(TEST_REPO, `show ${mainBranch}:${fileName}`)).toThrow();

    // And the project's main checkout must be back on the origin branch —
    // the merge temporarily switched it to the custom base, then restored
    // it. Poll: parallel specs may be mid-merge on the shared repo.
    await expect
      .poll(() => gitIn(TEST_REPO, 'rev-parse', '--abbrev-ref', 'HEAD'), {
        timeout: 15000,
        message: 'main checkout should be restored to the origin branch',
      })
      .toBe(mainBranch);
  });
});
