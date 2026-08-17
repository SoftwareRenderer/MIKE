import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

// ============================================================
// TOGGLE-FULLSCREEN SHORTCUT (default Ctrl+Up, Settings → Preferences)
// ============================================================
// The focused area toggles its maximize/minimize (fullscreen) state:
// chat / diff in the task view, the terminal panel. The binding is the
// default (no config mutation) — rebind behavior is covered in
// preferences-keyboard.spec.ts (isolated project).
// Chromium project, serial (shared fixture).

let state: { projectId: string; taskId: string } | null = null;

/** Wait until the diff file list is non-empty and stable — the 2s worktree
 *  watcher can re-fetch the manifest, clearing the list mid-test otherwise. */
async function waitForStableHeaders(page: Page): Promise<string> {
    const paths = () => page.evaluate(() =>
        Array.from(document.querySelectorAll('.column-diff button.diff-file-header .diff-file-path')).map(el => el.textContent ?? '').join('|'),
    );
    let prev = '';
    for (let i = 0; i < 30; i++) {
        const cur = await paths();
        if (cur && cur === prev) return cur;
        prev = cur;
        await page.waitForTimeout(1000);
    }
    throw new Error('diff file list never stabilized');
}

/** Full fixture: in-progress task with a worktree containing one file. */
async function setupFixture(page: Page): Promise<{ projectId: string; taskId: string }> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `toggle-fs-${Date.now()}`);
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
    // then add one untracked file -> one diff file section.
    const worktreePath = `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
        try { execSync(`test -d ${worktreePath}`); break; } catch { await new Promise(r => setTimeout(r, 500)); }
    }
    const file = 'togglefs-a.txt';
    execSync(`echo "hello ${file}" > ${worktreePath}/${file}`);
    await page.waitForFunction(async ({ pid, tid, f }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        const untracked = (data.untracked || []).map((u: any) => u.path);
        const tracked = Object.keys(data.files || {});
        return untracked.some((p: string) => p.includes(f)) || tracked.some((p: string) => p.includes(f));
    }, { pid: projectId, tid: taskId, f: file }, { timeout: 15000, polling: 1000 });

    return { projectId, taskId };
}

/** Exact classList of the task-detail root (regex class asserts are unsafe:
 *  "chat-fullscreen" contains "fullscreen"). */
function detailClasses(page: Page) {
    return page.evaluate(() => Array.from((document.querySelector('.task-detail-page') as HTMLElement).classList));
}

test.describe.serial('Toggle-fullscreen shortcut (Ctrl+Up)', () => {
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
        await page.waitForSelector('.task-detail-page', { timeout: 15000 });
    });

    test('chat: Ctrl+Up with focus in the chat column toggles chat fullscreen', async ({ page }) => {
        const detail = page.locator('.task-detail-page');
        const input = page.locator('.column-chat textarea');
        await input.focus();

        await page.keyboard.press('Control+ArrowUp');
        await expect(detail).toHaveClass(/chat-fullscreen/);

        // Toggle back off (focus is still in the chat column).
        await page.keyboard.press('Control+ArrowUp');
        await expect(detail).not.toHaveClass(/chat-fullscreen/);
    });

    test('diff: Ctrl+Up with focus in the diff column toggles diff fullscreen', async ({ page }) => {
        await waitForStableHeaders(page);
        const detail = page.locator('.task-detail-page');
        await page.locator('.column-diff button.diff-file-header').first().focus();

        await page.keyboard.press('Control+ArrowUp');
        expect(await detailClasses(page)).toContain('fullscreen');
        expect(await detailClasses(page)).not.toContain('chat-fullscreen');

        // Toggle back off.
        await page.keyboard.press('Control+ArrowUp');
        expect(await detailClasses(page)).not.toContain('fullscreen');
    });

    test('board column: Ctrl+Up with focus on the overview does nothing', async ({ page }) => {
        const detail = page.locator('.task-detail-page');
        await page.evaluate(() => (document.querySelector('.column-context') as HTMLElement).focus());

        await page.keyboard.press('Control+ArrowUp');
        const classes = await detailClasses(page);
        expect(classes).not.toContain('fullscreen');
        expect(classes).not.toContain('chat-fullscreen');

        // Plain ArrowUp still navigates overview cards (shortcut does not swallow it).
        await page.keyboard.press('ArrowUp');
        await page.waitForTimeout(200);
        expect(await detailClasses(page)).not.toContain('fullscreen');
    });

    test('terminal: Ctrl+Up with the panel focused toggles terminal fullscreen', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const panel = page.locator('.terminal-panel');

        await page.keyboard.press('Control+`');
        await expect(panel).toBeVisible({ timeout: 10000 });

        // The panel owns focus (xterm autofocuses on open).
        const focusInPanel = () => page.evaluate(() => {
            const el = document.querySelector('.terminal-panel');
            return !!el && el.contains(document.activeElement);
        });
        await expect.poll(focusInPanel).toBe(true);

        // Tooltip shows the binding.
        await expect(page.locator('.terminal-fullscreen-toggle')).toHaveAttribute('title', 'Toggle fullscreen (Ctrl+Up)');

        await page.keyboard.press('Control+ArrowUp');
        await expect(panel).toHaveClass(/fullscreen/);

        // Toggle back off.
        await page.keyboard.press('Control+ArrowUp');
        await expect(panel).not.toHaveClass(/fullscreen/);

        await page.locator('.terminal-panel .close-btn').click();
        await expect(panel).not.toBeVisible({ timeout: 5000 });
    });
});
