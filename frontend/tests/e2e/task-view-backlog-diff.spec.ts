import { test, expect, Page } from '@playwright/test';
import { unlinkSync } from 'fs';
import { join } from 'path';
import {
    getProjectId, createTaskViaApi, navigateToTask, csrfFetch,
    writeFileInWorktree, worktreeExists, getWorktreePath,
} from './helpers';

// --- Shared utilities ---

async function getTasks(page: Page, projectId: string): Promise<any[]> {
    return page.evaluate(async (pid: string) => {
        const res = await fetch(`/api/projects/${pid}/tasks`);
        const data = await res.json();
        return Array.isArray(data) ? data : (data?.tasks || []);
    }, projectId);
}

/**
 * Ensure an in-progress task with a worktree exists: find one, or start the
 * last task with the mock LLM and wait for the executor to create its worktree.
 * Returns { projectId, taskId }.
 */
async function ensureActiveWorktree(page: Page): Promise<{ projectId: string; taskId: string }> {
    const projectId = await getProjectId(page);
    const find = async () =>
        (await getTasks(page, projectId)).find((t: any) => t.status === 'in_progress' && t.branch);

    let task = await find();
    if (!task) {
        const tasks = await getTasks(page, projectId);
        const last = tasks[tasks.length - 1];
        if (!last) throw new Error('no tasks in test project to start');

        await csrfFetch(page, `/api/projects/${projectId}/tasks/${last.id}/move`, {
            method: 'POST',
            body: { status: 'in_progress' },
        });
        await page.evaluate(async ({ pid, tid }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: 'list files' }),
            });
        }, { pid: projectId, tid: last.id });

        // Poll until the executor created the worktree (branch gets set on the task)
        await page.waitForFunction(async (pid: string) => {
            const res = await fetch(`/api/projects/${pid}/tasks`);
            const data = await res.json();
            const tasks = Array.isArray(data) ? data : (data?.tasks || []);
            return tasks.some((t: any) => t.branch);
        }, projectId, { timeout: 20000, polling: 1000 });
        task = await find();
    }
    if (!task) throw new Error('no in-progress task with a branch found');
    if (!worktreeExists(projectId, task.id)) throw new Error('worktree directory not found for active task');
    return { projectId, taskId: task.id };
}

test.describe.serial('task view diff panel for backlog tasks', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(30000);
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('fresh backlog task: no "loading diff viewer", shows "No changes yet"', async ({ page }) => {
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `backlog-diff-${Date.now()}`);
        await navigateToTask(page, taskId, projectId);

        // The diff panel must not show a loading placeholder — nothing is loading.
        await expect(page.locator('.diff-viewer-loading')).not.toBeVisible();
        // And no lazily-loaded DiffViewer should have been rendered either.
        await expect(page.locator('.diff-empty')).not.toBeVisible();
        // Instead, the backlog-appropriate empty state is shown.
        await expect(page.locator('.column-diff .diff-viewer-empty')).toHaveText('No changes yet');
    });

    test('task sent back to backlog: diff panel shows its changes', async ({ page }) => {
        const { projectId, taskId } = await ensureActiveWorktree(page);

        // Leave a recognizable change in the worktree.
        const fileName = `sent-back-${Date.now()}.txt`;
        writeFileInWorktree(projectId, taskId, fileName, 'sent back to backlog\n');

        // Move the task back to the backlog (worktree/branch persist).
        const moveRes = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
            method: 'POST',
            body: { status: 'backlog' },
        });
        expect(moveRes.ok).toBe(true);

        try {
            // Fresh load of the task view (board re-fetch enriches worktree_exists).
            await navigateToTask(page, taskId, projectId);

            // No stuck loading placeholder — the diff viewer actually loads.
            await expect(page.locator('.diff-viewer-loading')).not.toBeVisible({ timeout: 15000 });
            // And the worktree's changes are visible in the diff panel.
            await expect(async () => {
                const diffText = await page.locator('.column-diff').textContent().catch(() => '');
                expect(diffText).toContain(fileName);
            }).toPass({ timeout: 10000 });
        } finally {
            try {
                unlinkSync(join(getWorktreePath(projectId, taskId), fileName));
            } catch { /* ignore */ }
        }
    });
});
