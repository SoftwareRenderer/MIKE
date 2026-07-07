import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function getWorktreePath(taskId: string): string {
    const config = getConfig();
    return `${config.workspace_dir}/${taskId}`;
}

function writeFileInWorktree(taskId: string, filePath: string, content: string) {
    const worktreePath = getWorktreePath(taskId);
    execSync(`mkdir -p ${worktreePath}`);
    execSync(`echo "${content}" > ${worktreePath}/${filePath}`);
}

function worktreeExists(taskId: string): boolean {
    try {
        execSync(`test -d ${getWorktreePath(taskId)}/.git`);
        return true;
    } catch {
        return false;
    }
}

test.describe.configure({ mode: 'parallel' });
test.describe('Thread Switch Diff Alignment', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('diff panel shows correct content after multiple thread switches', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `thread-diff-${Date.now()}`);

    // Start the task to trigger worktree creation
    await ensureTaskStarted(page, projectId, taskId);

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Get task data (includes branch info)
    const taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    // If worktree doesn't exist, we can't test diff content
    if (!worktreeExists(taskId)) {
        // Try to get project path from task data
        const projectPath = taskData?.project_path;
        if (projectPath) {
            const branchName = taskData.branch || `task/${taskId}`;
            const worktreePath = getWorktreePath(taskId);
            execSync(`cd ${projectPath} && git worktree add --detach ${worktreePath} HEAD 2>&1 || true`);
            execSync(`cd ${worktreePath} && git checkout -B ${branchName} 2>&1 || true`);
        } else {
            test.skip('Worktree not available for diff test');
        }
    }

    // Get the main thread
    const threadsResult = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const mainThreadId = threadsResult[0]?.id;
    expect(mainThreadId).toBeTruthy();

    // Write Thread A's file
    writeFileInWorktree(taskId, 'file-a.txt', 'hello friend');

    // Wait for diff to pick up the new file
    await page.waitForTimeout(1000);

    // Create a checkpoint message (needed for fork)
    const msgResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: 'Change world to friend' },
    });
    expect(msgResult.ok).toBe(true);
    const msgId = msgResult.data?.id;

    // Fork to create Thread B (saves current worktree, resets to checkpoint)
    const forkResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/fork`, {
      method: 'POST',
      body: { from_thread_id: mainThreadId, from_message_id: msgId },
    });
    expect(forkResult.ok).toBe(true);
    const forkThreadId = forkResult.data?.id;

    // After fork, worktree is reset. Write Thread B's file.
    writeFileInWorktree(taskId, 'file-b.txt', 'hello choom');

    // Wait for the diff panel to update
    await page.waitForTimeout(1500);

    // Verify we're on the fork thread and see its file
    await expect(page.locator('.diff-content')).toContainText('file-b.txt', { timeout: 10000 });

    // --- Switch 1: Fork → Main ---
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    await page.waitForTimeout(1500);

    // Verify diff panel shows Thread A's file
    await expect(page.locator('.diff-content')).toContainText('file-a.txt', { timeout: 10000 });

    // --- Switch 2: Main → Fork ---
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: forkThreadId },
    });
    await page.waitForTimeout(1500);

    // Verify diff panel shows Thread B's file
    await expect(page.locator('.diff-content')).toContainText('file-b.txt', { timeout: 10000 });

    // --- Switch 3: Fork → Main ---
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    await page.waitForTimeout(1500);

    // Verify diff panel still shows Thread A's file (not corrupted)
    await expect(page.locator('.diff-content')).toContainText('file-a.txt', { timeout: 10000 });

    // --- Switch 4: Main → Fork ---
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: forkThreadId },
    });
    await page.waitForTimeout(1500);

    // Verify diff panel still shows Thread B's file (not corrupted)
    await expect(page.locator('.diff-content')).toContainText('file-b.txt', { timeout: 10000 });
  });
});
