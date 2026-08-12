import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, csrfFetch } from './helpers';

/**
 * Offline caching: previously viewed chat messages and the current task diff
 * must remain visible when the server becomes unreachable for those reads.
 *
 * Technique: after viewing the content online (which populates the IndexedDB
 * cache), we use page.route(...).abort() to simulate the endpoint being
 * unreachable (same pattern as ws-stale-reconnect.spec.ts), then re-open the
 * content and assert it still renders from the cache.
 */

// ─── Diff helpers ────────────────────────────────────────────────────────────

/**
 * The e2e server is started with MIKE_WORKTREES_DIR=/tmp/kanban-test-worktrees
 * (see global-setup.ts). Worktrees are created there at {worktreesDir}/{projectId}/{taskId}.
 * NOTE: /api/config returns the DB-stored workspace_dir (the default .worktrees), which
 * diverges from the env override actually used for worktree creation — so we hardcode the
 * env value here rather than trusting config.workspace_dir.
 */
const WORKTREES_DIR = '/tmp/kanban-test-worktrees';

/** Worktree path for a task: {worktreesDir}/{projectId}/{taskId}. */
function worktreePath(projectId: string, taskId: string): string {
    return `${WORKTREES_DIR}/${projectId}/${taskId}`;
}

function writeFileInWorktree(worktreeDir: string, filePath: string, content: string) {
    execSync(`mkdir -p ${worktreeDir}`);
    execSync(`echo "${content}" > ${worktreeDir}/${filePath}`);
}

/** Initialize a task's worktree via the API (no LLM needed). */
async function initWorktree(page: Page, projectId: string, taskId: string): Promise<void> {
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/init`, { method: 'POST' });
    expect(result.ok, `init worktree failed: ${JSON.stringify(result.data)}`).toBe(true);
}

/** Poll the diff API until the expected file appears in the manifest. */
async function waitForDiffFile(page: Page, projectId: string, taskId: string, expectFile: string): Promise<void> {
    await expect(async () => {
        const present = await page.evaluate(async ({ pid, tid, file }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
            if (!res.ok) return false;
            const data = await res.json();
            const inFiles = Object.keys(data.files || {}).some((k: string) => k.includes(file));
            const inUntracked = (data.untracked || []).some((u: any) => (u.path || '').includes(file));
            return inFiles || inUntracked;
        }, { pid: projectId, tid: taskId, file: expectFile });
        expect(present).toBe(true);
    }).toPass({ timeout: 15000 });
}

test.describe.serial('Offline cache', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('previously viewed chat messages remain visible when the messages endpoint is unreachable', async ({ page }) => {
        test.setTimeout(90000);
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `offline-msg-${Date.now()}`);
        await ensureTaskStarted(page, projectId, taskId);

        // Send the message via the API (same proven pattern as chat-messages.spec.ts),
        // so it exists in the DB before the chat is opened.
        const msgContent = `offline-msg-${Date.now()}`;
        const send = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/send`, {
            method: 'POST',
            body: { content: msgContent },
        });
        expect(send.ok).toBe(true);

        // Open the task — loadHistory runs getTaskMessages online and caches all messages (incl. msgContent).
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.chat-message').filter({ hasText: msgContent })).toBeVisible({ timeout: 10000 });

        // Simulate the messages endpoint becoming unreachable.
        await page.route(`**/api/projects/*/tasks/${taskId}/messages*`, (route) => route.abort());

        // Re-open the task — getTaskMessages is aborted and must fall back to the cached copy.
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.chat-message').filter({ hasText: msgContent })).toBeVisible({ timeout: 10000 });
    });

    test('previously viewed task diff remains visible when the diff endpoint is unreachable', async ({ page }) => {
        test.setTimeout(90000);
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `offline-diff-${Date.now()}`);

        // Create the worktree via the API (no LLM needed).
        await initWorktree(page, projectId, taskId);

        const wtDir = worktreePath(projectId, taskId);

        const fileName = `offline-diff-${Date.now()}.txt`;
        writeFileInWorktree(wtDir, fileName, 'hello offline');

        // Move the task to in_progress via a direct status update. This does NOT
        // start the executor (so it won't auto-commit / move the task to review),
        // but it satisfies loadDiff's non-backlog requirement.
        const move = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}`, {
            method: 'PUT',
            body: { status: 'in_progress' },
        });
        expect(move.ok, `move to in_progress failed: ${JSON.stringify(move.data)}`).toBe(true);

        // Wait for the backend to pick up the file.
        await waitForDiffFile(page, projectId, taskId, fileName);

        // Open the task — loadDiff runs getTaskDiff online and populates the cache.
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.diff-content')).toContainText(fileName, { timeout: 15000 });

        // Simulate the diff endpoints becoming unreachable.
        await page.route(`**/api/projects/*/tasks/${taskId}/diff*`, (route) => route.abort());

        // Re-open the task — getTaskDiff is aborted and must fall back to the cached copy.
        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.diff-content')).toContainText(fileName, { timeout: 15000 });
    });
});
