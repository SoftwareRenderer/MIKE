import { test, expect } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted } from './helpers';

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

function getWorktreePath(taskId: string): string {
    // Worktrees are stored under WORKTREES_DIR env var, not workspace_dir config.
    // The test server sets WORKTREES_DIR=/tmp/kanban-test-worktrees
    return `/tmp/kanban-test-worktrees/${taskId}`;
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

/**
 * Poll the diff API until the expected file appears, then re-navigate to the task
 * so the diff panel renders fresh data. The worktree watcher's 2s poll + WS broadcast
 * is unreliable in headless tests, so we force a reload once the backend confirms
 * the file is visible.
 */
async function waitForDiffFile(page: Page, projectId: string, taskId: string, expectFile: string) {
    await page.waitForFunction(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        return data.files && Object.keys(data.files).some((k: string) => k.includes(file));
    }, [projectId, taskId, expectFile], { timeout: 15000 });
    await navigateToTask(page, taskId);
    await expect(page.locator('.diff-content')).toContainText(expectFile, { timeout: 15000 });
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

    // Wait for executor to create the worktree (poll up to 15s)
    let taskData = null;
    for (let i = 0; i < 30; i++) {
        taskData = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            return res.ok ? res.json() : null;
        }, { pid: projectId, tid: taskId });
        if (taskData?.branch && worktreeExists(taskId)) break;
        await page.waitForTimeout(500);
    }

    // If worktree still doesn't exist, create one manually
    if (!worktreeExists(taskId)) {
        // Get project path from projects API
        const projects = await page.evaluate(async (pid: string) => {
            const res = await fetch(`/api/projects`);
            const all = await res.json();
            return all.find((p: any) => p.id === pid);
        }, projectId);
        const projectPath = projects?.path;
        if (projectPath) {
            const branchName = taskData?.branch || `task/${taskId}`;
            const worktreePath = getWorktreePath(taskId);
            execSync(`cd ${projectPath} && git worktree add --detach ${worktreePath} HEAD 2>&1 || true`);
            execSync(`cd ${worktreePath} && git checkout -B ${branchName} 2>&1 || true`);
            // Update task with branch info
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
                method: 'PUT',
                body: { branch: branchName, base_ref: 'master' },
            });
        } else {
            test.skip(`Worktree not available for diff test (projectPath=${projectPath})`);
        }
    }

    // Get the main thread
    const threadsResult = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const mainThreadId = threadsResult[0]?.id;
    expect(mainThreadId).toBeTruthy();

    // --- Thread A (Main): write file-a.txt ---
    writeFileInWorktree(taskId, 'file-a.txt', 'hello friend');
    await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

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

    // --- Thread B (fork): write file-b.txt ---
    writeFileInWorktree(taskId, 'file-b.txt', 'hello choom');
    await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

    // --- Round-trip switches (matches backend TestThreadSwitchWorktreeContent) ---
    // Switch 1: Fork → Main
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

    // Switch 2: Main → Fork
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: forkThreadId },
    });
    await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

    // Switch 3: Fork → Main
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    await waitForDiffFile(page, projectId, taskId, 'file-a.txt');

    // Switch 4: Main → Fork
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: forkThreadId },
    });
    await waitForDiffFile(page, projectId, taskId, 'file-b.txt');

    // Switch 5: Fork → Main (final)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/threads/switch`, {
      method: 'POST',
      body: { thread_id: mainThreadId },
    });
    await waitForDiffFile(page, projectId, taskId, 'file-a.txt');
  });
});
