import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { existsSync, writeFileSync, mkdirSync } from 'fs';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask, getWorktreePath, worktreeExists,
  isolatedProject, gotoProject, ownProjectPath } from './helpers';

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
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('untracked worktree files stay in the worktree, staged files land in the project', async ({ page }) => {
    const unique = Date.now();
    const stagedName = `cdn-staged-${unique}.txt`;
    const untrackedName = `cdn-untracked-${unique}.txt`;

    const taskId = await createTaskViaApi(page, projectId, `cdn-unstaged-${unique}`);
    expect(taskId).not.toBe('');

    // Start the task so its worktree is created
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });
    expect(moveResult.ok).toBe(true);

    // Poll for the worktree (graceful skip if the environment can't create one)
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

    // A staged file (committed on done) and an untracked file (must NOT be)
    writeStagedFile(projectId, taskId, stagedName, 'staged content\n');
    writeUntrackedFile(projectId, taskId, untrackedName, 'unstaged content\n');

    // Move to review (stops the executor so nothing else touches the worktree)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task and wait for the review bar
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Wait for the "Commit and move to Done" button — it only renders once
    // diff stats report staged changes, so this also proves the changes are
    // visible to the client.
    const commitBtn = page.locator('.btn-commit-and-done');
    await expect(commitBtn).toBeVisible({ timeout: 15000 });

    // Click "Commit and move to done"
    await commitBtn.click();

    // Commit dialog should appear
    const commitDialog = page.locator('[role="dialog"]');
    await expect(commitDialog).toBeVisible({ timeout: 10000 });

    // Fill the commit message (also cancels streaming generation)
    const textarea = page.locator('.commit-textarea');
    await expect(textarea).toBeVisible({ timeout: 15000 });
    await textarea.fill(`cdn test commit ${unique}`);

    const confirmBtn = page.locator('.modal-footer .btn-primary');
    await expect(confirmBtn).toBeEnabled({ timeout: 15000 });
    await confirmBtn.click();

    // Wait for the task to move to done
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 15000 });

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
