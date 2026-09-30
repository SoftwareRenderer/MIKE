import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync } from 'fs';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask, getWorktreePath, waitForWorktree,
  waitForTaskState, runHasEnded, isolatedProject, gotoProject, ownProjectPath } from './helpers';

// Test project and worktrees locations (managed by global-setup.ts)
// This spec commits through the app and then reads the PROJECT checkout, so it must not
// share that checkout with anyone: other specs run their own `git add -A && git commit`
// in the shared test repo, and an index race there shows up here as "the staged file is
// not in the project" — the exact assertion this spec exists for.
isolatedProject();

const TEST_REPO = () => ownProjectPath() || '/tmp/kanban-test-repo';

/** Write a file into the task worktree and stage it (git add). */
function writeStagedFile(projectId: string, taskId: string, filePath: string, content: string): void {
  const wt = getWorktreePath(projectId, taskId);
  mkdirSync(wt, { recursive: true });
  writeFileSync(`${wt}/${filePath}`, content);
  execSync(`cd ${wt} && git add ${filePath}`);
}

/** Write a file into the task worktree WITHOUT staging it (untracked). */
function writeUntrackedFile(projectId: string, taskId: string, filePath: string, content: string): void {
  const wt = getWorktreePath(projectId, taskId);
  mkdirSync(wt, { recursive: true });
  writeFileSync(`${wt}/${filePath}`, content);
}

function projectGit(args: string): string {
  return execSync(`git -C ${TEST_REPO()} ${args}`, { encoding: 'utf-8' }).trim();
}

test.describe.serial('Commit and move to done excludes unstaged files', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await page.waitForSelector('.project-item', { timeout: 60000 });
    projectId = await getProjectId(page);
  });

  test('untracked worktree files stay in the worktree, staged files land in the project', async ({ page }) => {
    test.setTimeout(180000);
    const unique = Date.now();
    const stagedName = `cdn-staged-${unique}.txt`;
    const untrackedName = `cdn-untracked-${unique}.txt`;

    const taskId = await createTaskViaApi(page, projectId, `cdn-unstaged-${unique}`);
    expect(taskId).not.toBe('');

    // Start the task. Its own prompt runs as soon as it is in progress and that turn is what moves it to
    // Review: the worktree exists only once the start has begun, and the review bar offers "Commit and move to
    // Done" only once the assistant has answered. Moving the task to review by hand stops the turn, and a
    // transcript ending on the user's row is the one state that bar answers with "Resume".
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    expect(moveResult.ok).toBe(true);
    await waitForWorktree(projectId, taskId);
    await waitForTaskState(page, projectId, taskId, runHasEnded, 120000);

    // Write only now that the run has ended: the executor is still inside its own git calls while it works,
    // and a `git add` that lands before it finishes is an index it can rewrite.
    writeStagedFile(projectId, taskId, stagedName, 'staged content\n');
    writeUntrackedFile(projectId, taskId, untrackedName, 'unstaged content\n');

    // Navigate to the task and wait for the review bar
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 60000 });

    // Wait for the "Commit and move to Done" button — it only renders once
    // diff stats report staged changes, so this also proves the changes are
    // visible to the client.
    const commitBtn = page.locator('.btn-commit-and-done');
    await expect(commitBtn).toBeVisible({ timeout: 60000 });

    // Click "Commit and move to done"
    await commitBtn.click();

    // Commit dialog should appear
    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 60000 });

    // Fill the commit message (also cancels streaming generation)
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 60000 });
    await textarea.fill(`cdn test commit ${unique}`);

    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 60000 });
    await confirmBtn.click();

    // Wait for the task to move to done
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 60000 });

    // The staged file must be committed and merged into the project checkout.
    expect(existsSync(`${TEST_REPO()}/${stagedName}`)).toBe(true);
    expect(projectGit(`log --all --oneline -- ${stagedName}`)).not.toBe('');

    // The untracked file must NOT have landed in the main project folder:
    // not on disk, not in any ref, not in the project's git status.
    expect(existsSync(`${TEST_REPO()}/${untrackedName}`)).toBe(false);
    expect(projectGit(`log --all --oneline -- ${untrackedName}`)).toBe('');
    expect(projectGit('status --porcelain')).not.toContain(untrackedName);

    // And it must still exist in the task's local worktree.
    expect(existsSync(`${getWorktreePath(projectId, taskId)}/${untrackedName}`)).toBe(true);
  });
});
