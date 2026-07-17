import { Page } from '@playwright/test';
import { execSync } from 'child_process';
import { readdirSync, statSync } from 'fs';
import { join } from 'path';
import { navigateToTask } from './helpers';

// --- Shared utilities ---

function getConfig() {
    return JSON.parse(execSync('curl -s http://localhost:3000/api/config', { encoding: 'utf-8' }));
}

export function findWorktreeDir(): string | null {
    const worktreesDir = getConfig().workspace_dir;
    try {
        const projectEntries = readdirSync(worktreesDir);
        for (const projEntry of projectEntries) {
            const projPath = join(worktreesDir, projEntry);
            if (!statSync(projPath).isDirectory()) continue;
            const taskEntries = readdirSync(projPath);
            for (const taskEntry of taskEntries) {
                const fullPath = join(projPath, taskEntry);
                if (statSync(fullPath).isDirectory()) {
                    return fullPath;
                }
            }
        }
    } catch { /* ignore */ }
    return null;
}

export function getTaskInfo(): { projectId: string; taskId: string } | null {
    try {
        const projects = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects`, { encoding: 'utf-8' }
        ));
        if (!projects.length) return null;
        const projectId = projects[0].id;
        const tasks = JSON.parse(execSync(
            `curl -s http://localhost:3000/api/projects/${projectId}/tasks`, { encoding: 'utf-8' }
        ));
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (!task) return null;
        return { projectId, taskId: task.id };
    } catch {
        return null;
    }
}

export function getDiffViaApi(projectId: string, taskId: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff"`,
        { encoding: 'utf-8' }
    ));
}

export function getDiffFileViaApi(projectId: string, taskId: string, filePath: string): any {
    return JSON.parse(execSync(
        `curl -s "http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/diff/file?path=${encodeURIComponent(filePath)}"`,
        { encoding: 'utf-8' }
    ));
}

/**
 * Ensure an in-progress task with a worktree exists.
 * If none exists, moves the last task to in_progress and triggers lazy-start.
 * Returns { taskInfo, worktreeDir } or null if unavailable.
 */
export async function ensureActiveWorktree(page: Page): Promise<{ taskInfo: { projectId: string; taskId: string }; worktreeDir: string } | null> {
    let taskInfo = getTaskInfo();
    let worktreeDir = findWorktreeDir();

    if (!taskInfo || !worktreeDir) {
        const projectId = await page.evaluate(async () => {
            const res = await fetch('/api/projects');
            const projects = await res.json();
            return projects[0]?.id || '';
        });
        const taskId = await page.evaluate(async ({ pid }: { pid: string }) => {
            const res = await fetch(`/api/projects/${pid}/board`);
            const board = await res.json();
            return board.tasks[board.tasks.length - 1]?.id || '';
        }, { pid: projectId });

        await page.evaluate(async ({ pid, tid }) => {
            const csrf = (window as any).__csrfToken || '';
            await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    ...(csrf ? { 'X-CSRF-Token': csrf } : {}),
                },
                credentials: 'include',
                body: JSON.stringify({ status: 'in_progress' }),
            });
        }, { pid: projectId, tid: taskId });

        await page.evaluate(async ({ pid, tid }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ content: 'list files' }),
            });
        }, { pid: projectId, tid: taskId });

        // Poll for worktree directory to appear (condition-based, no waitForTimeout)
        for (let i = 0; i < 20; i++) {
            worktreeDir = findWorktreeDir();
            if (worktreeDir) break;
            await new Promise(resolve => setTimeout(resolve, 1000));
        }

        const tasksData = JSON.parse(execSync(
            `curl -s "http://localhost:3000/api/projects/${projectId}/tasks"`, { encoding: 'utf-8' }
        ));
        const tasks = Array.isArray(tasksData) ? tasksData : (tasksData?.tasks || []);
        const task = tasks.find((t: any) => t.status === 'in_progress' && t.branch);
        if (task) {
            taskInfo = { projectId, taskId: task.id };
        }
    }

    if (!taskInfo || !worktreeDir) return null;
    return { taskInfo, worktreeDir };
}

// --- Thread-switch utilities ---

export function getWorktreePath(taskId: string): string {
    return `/tmp/kanban-test-worktrees/${taskId}`;
}

export function writeFileInWorktree(taskId: string, filePath: string, content: string) {
    const worktreePath = getWorktreePath(taskId);
    execSync(`mkdir -p ${worktreePath}`);
    execSync(`echo "${content}" > ${worktreePath}/${filePath}`);
}

export function worktreeExists(taskId: string): boolean {
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
export async function waitForDiffFile(page: Page, projectId: string, taskId: string, expectFile: string) {
    await page.waitForFunction(async ([pid, tid, file]: [string, string, string]) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        return data.files && Object.keys(data.files).some((k: string) => k.includes(file));
    }, [projectId, taskId, expectFile], { timeout: 15000 });
    await navigateToTask(page, taskId);
    await page.locator('.diff-content').waitFor({ state: 'visible', timeout: 15000 });
}

/**
 * Poll the diff API until the given predicate matches, then re-navigate to the task
 * to refresh the UI. Replaces all waitForTimeout calls after file system changes.
 */
export async function waitForDiffUpdate(
    page: Page,
    projectId: string,
    taskId: string,
    predicate: (diff: any) => boolean,
    timeout = 15000
) {
    const startTime = Date.now();
    while (Date.now() - startTime < timeout) {
        const diff = await page.evaluate(async ([pid, tid]: [string, string]) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
            return res.json();
        }, [projectId, taskId]);
        if (predicate(diff)) break;
        await new Promise(resolve => setTimeout(resolve, 500));
    }
    await navigateToTask(page, taskId);
    await page.locator('.diff-content').waitFor({ state: 'visible', timeout: 10000 });
}
