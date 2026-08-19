import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

// ============================================================
// DIFF PATH TRUNCATION
// ============================================================
// Truncated diff file paths keep the file name (last segment) whole —
// the ellipsis lands in the directory part. Chromium project (no config mutation).

const LONG_DIR = 'frontend/src/lib/components/CardDetail';
const FILENAME = 'CheckpointOverlay.svelte';
const LONG_PATH = `${LONG_DIR}/${FILENAME}`; // 64 chars > 48-char display limit

let state: { projectId: string; taskId: string } | null = null;

/** Full fixture: in-progress task with a worktree containing one file
 *  under a long nested path. */
async function setupFixture(page: Page): Promise<{ projectId: string; taskId: string }> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `path-trunc-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
        method: 'POST',
        body: { status: 'in_progress' },
    });

    // Trigger lazy start (mock LLM "list files" fixture) so a worktree + branch exist.
    await page.evaluate(async ({ pid, tid }) => {
        await fetch(`/api/projects/${pid}/tasks/${tid}/send`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ content: 'list files' }),
        });
    }, { pid: projectId, tid: taskId });

    await page.waitForFunction(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks`);
        const tasks = await res.json();
        return tasks.some((t: any) => t.id === tid && t.branch);
    }, { pid: projectId, tid: taskId }, { timeout: 20000, polling: 1000 });

    // Wait for the worktree dir (branch set does not imply it exists yet),
    // then add one untracked file under a long nested path.
    const worktreePath = `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
        try { execSync(`test -d ${worktreePath}`); break; } catch { await new Promise(r => setTimeout(r, 500)); }
    }
    execSync(`mkdir -p ${worktreePath}/${LONG_DIR} && echo "checkpoint diff" > ${worktreePath}/${LONG_PATH}`);

    await page.waitForFunction(async ({ pid, tid, p }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        const untracked = (data.untracked || []).map((u: any) => u.path);
        const tracked = Object.keys(data.files || {});
        return untracked.some((x: string) => x.includes(p)) || tracked.some((x: string) => x.includes(p));
    }, { pid: projectId, tid: taskId, p: LONG_PATH }, { timeout: 15000, polling: 1000 });

    return { projectId, taskId };
}

test.describe.serial('Diff path truncation', () => {
    test.beforeEach(async ({ page }) => {
        test.setTimeout(60000);
        if (!state) {
            await page.goto('/');
            await page.waitForSelector('.board .column', { timeout: 10000 });
            state! = await setupFixture(page);
        } else {
            await page.goto('/');
        }
        await navigateToTask(page, state.taskId);
    });

    test('keeps the file name whole when the path is truncated', async ({ page }) => {
        const pathEl = page.locator('.column-diff button.diff-file-header .diff-file-path[title*="CheckpointOverlay.svelte"]');
        await expect(pathEl).toBeVisible({ timeout: 15000 });

        // Wait for a stable reading — the 2s worktree watcher can re-render the
        // file list mid-test.
        let text = '';
        let prev = '';
        for (let i = 0; i < 30; i++) {
            text = (await pathEl.textContent()) ?? '';
            if (text && text === prev) break;
            prev = text;
            await page.waitForTimeout(500);
        }

        const title = (await pathEl.getAttribute('title')) ?? '';

        // The title tooltip always carries the full untruncated path.
        expect(title).toContain(LONG_PATH);

        // Truncated (64-char path, 48 limit): ellipsis in the directory part,
        // name whole, tail starts at a '/' boundary.
        expect(text.length).toBeLessThanOrEqual(48);
        expect(text).toContain('…');
        expect(text).toContain(FILENAME);
        expect(text.slice(text.indexOf('…') + 1).startsWith('/')).toBe(true);
        // The tail keeps at least one trailing directory segment, not just the bare name.
        expect(text).toContain('/CardDetail/CheckpointOverlay.svelte');
        // Regression: old middle-truncation cut the name (…eckpointOverlay) —
        // the ellipsis must never touch the name.
        expect(text.slice(0, text.indexOf(FILENAME))).toMatch(/\/$/);
    });
});
