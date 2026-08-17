import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import { getProjectId, createTaskViaApi, navigateToTask, csrfFetch } from './helpers';

// ============================================================
// TASK VIEW COLUMN NAVIGATION (Board / Chat / Diff)
// ============================================================
// Left/Right cycle focus Board -> Chat -> Diff -> Board (cyclic); Up/Down
// dispatches on the focused column (cards / page-scroll / file headers).
// Chromium project (no config mutation).

interface ActiveInfo {
    tag: string | null;
    inBoard: boolean;
    inChat: boolean;
    inDiff: boolean;
    isOverviewCard: boolean;
    isChatInput: boolean;
    isDiffHeader: boolean;
    isDiffColumn: boolean;
    cardId: string | null;
    headerPath: string | null;
}

function activeInfo(page: Page) {
    return page.evaluate(() => {
        const el = document.activeElement as HTMLElement | null;
        return {
            tag: el?.tagName ?? null,
            inBoard: !!(el && el.closest('.column-context')),
            inChat: !!(el && el.closest('.column-chat')),
            inDiff: !!(el && el.closest('.column-diff')),
            isOverviewCard: !!(el && el.closest('.column-context .task-wrapper > .card')),
            isChatInput: el?.tagName === 'TEXTAREA' && !!(el && el.closest('.column-chat')),
            isDiffHeader: el?.matches?.('button.diff-file-header') === true,
            isDiffColumn: el?.classList?.contains('column-diff') === true,
            cardId: el?.closest('[data-task-id]')?.getAttribute('data-task-id') ?? null,
            headerPath: el?.querySelector('.diff-file-path')?.textContent ?? null,
        } as ActiveInfo;
    });
}

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

/** Full fixture: in-progress task with a worktree containing two files. */
async function setupFixture(page: Page): Promise<{ projectId: string; taskId: string }> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `column-nav-${Date.now()}`);
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
    // then add two untracked files -> two diff file sections.
    const worktreePath = `/tmp/kanban-test-worktrees/${projectId}/${taskId}`;
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
        try { execSync(`test -d ${worktreePath}`); break; } catch { await new Promise(r => setTimeout(r, 500)); }
    }
    const files = ['colnav-a.txt', 'colnav-b.txt'];
    for (const f of files) {
        execSync(`echo "hello ${f}" > ${worktreePath}/${f}`);
    }
    await page.waitForFunction(async ({ pid, tid, fs }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}/diff`);
        const data = await res.json();
        const untracked = (data.untracked || []).map((u: any) => u.path);
        const tracked = Object.keys(data.files || {});
        return fs.every((f: string) => untracked.some((p: string) => p.includes(f)) || tracked.some((p: string) => p.includes(f)));
    }, { pid: projectId, tid: taskId, fs: files }, { timeout: 15000, polling: 1000 });

    return { projectId, taskId };
}

test.describe.serial('Task view column navigation', () => {
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
        await waitForStableHeaders(page);
    });

    test('Right cycles Board -> Chat -> Diff -> Board; Left cycles back', async ({ page }) => {
        const firstCard = page.locator('.column-context .task-wrapper > .card').first();
        await firstCard.focus();

        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true });

        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isDiffHeader: true, inDiff: true });

        // Wrap: Right from Diff -> Board
        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isOverviewCard: true, inBoard: true });

        // Left: Board -> Diff -> Chat -> Board (cyclic)
        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isDiffHeader: true });

        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true });

        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isOverviewCard: true, inBoard: true });
    });

    test('Up/Down move between overview cards when the Board column has focus', async ({ page }) => {
        const cards = page.locator('.column-context .task-wrapper > .card');
        expect(await cards.count()).toBeGreaterThanOrEqual(2);
        const firstId = await cards.nth(0).getAttribute('data-task-id');
        const secondId = await cards.nth(1).getAttribute('data-task-id');

        await cards.nth(0).focus();
        await page.keyboard.press('ArrowDown');
        expect((await activeInfo(page)).cardId).toBe(secondId);

        await page.keyboard.press('ArrowUp');
        expect((await activeInfo(page)).cardId).toBe(firstId);
    });

    test('Down/Up cycle diff file headers when the Diff column has focus', async ({ page }) => {
        const headers = page.locator('.column-diff button.diff-file-header');
        const count = await headers.count();
        const firstPath = await headers.nth(0).locator('.diff-file-path').textContent();
        const secondPath = await headers.nth(1).locator('.diff-file-path').textContent();
        expect(firstPath).not.toBe(secondPath);

        await headers.nth(0).focus();
        await page.keyboard.press('ArrowDown');
        expect((await activeInfo(page)).headerPath).toBe(secondPath);

        // Wrap at the end: last header -> first
        await headers.nth(count - 1).focus();
        await page.keyboard.press('ArrowDown');
        expect((await activeInfo(page)).headerPath).toBe(firstPath);

        // Wrap at the top: first header -> last
        await headers.nth(0).focus();
        await page.keyboard.press('ArrowUp');
        expect((await activeInfo(page)).headerPath).toBe(await headers.nth(count - 1).locator('.diff-file-path').textContent());
    });

    test('Down/Up page-scroll the chat when the Chat region has focus', async ({ page }) => {
        // Long messages guarantee overflow — content-visibility collapses
        // off-screen entries, so the initial content alone would not.
        const input = page.locator('.column-chat textarea');
        for (let i = 0; i < 3; i++) {
            await input.fill(`Scroll filler ${i}: ` + 'lorem ipsum dolor sit amet '.repeat(60));
            await input.press('Enter');
            await page.waitForTimeout(500); // let the mock response finish
        }
        await page.waitForFunction(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            return el.scrollHeight > 400;
        }, undefined, { timeout: 15000 });

        // Constrain the scroll container to one "page".
        await page.evaluate(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            el.style.maxHeight = '120px';
        });
        const scrollTop = () => page.evaluate(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            return el.scrollTop;
        });
        const clientHeight = await page.evaluate(() => (document.querySelector('.column-chat .chat-messages') as HTMLElement).clientHeight);
        expect(clientHeight).toBeGreaterThan(0);

        await page.evaluate(() => (document.querySelector('.column-chat .chat-messages') as HTMLElement).focus());
        await page.evaluate(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            el.scrollTop = 0;
        });
        await expect.poll(scrollTop, { timeout: 3000 }).toBe(0);

        // Page down settles one page down (not clamped — plenty below).
        await page.keyboard.press('ArrowDown');
        await expect.poll(scrollTop, { timeout: 5000 }).toBe(clientHeight);

        // Page up: settles back at the top.
        await page.keyboard.press('ArrowUp');
        await expect.poll(scrollTop, { timeout: 5000 }).toBe(0);

        // Down in the chat INPUT must not scroll (native text editing).
        await input.focus();
        const inputTop = await scrollTop();
        await page.keyboard.press('ArrowDown');
        await page.waitForTimeout(300);
        expect(await scrollTop()).toBe(inputTop);
    });

    test('Alt+ArrowRight does not switch columns (modifier gate)', async ({ page }) => {
        await page.evaluate(() => (document.querySelector('.column-context') as HTMLElement).focus());
        await page.keyboard.press('Alt+ArrowRight');
        const info = await activeInfo(page);
        expect(info.tag).toBe('DIV');
        expect(info.inBoard).toBeTruthy();
        expect(info.isOverviewCard).toBeFalsy();
    });

    test('Diff column with no files falls back to the column container', async ({ page }) => {
        // A backlog task has no branch/worktree -> no diff file sections.
        const backlogTask = await createTaskViaApi(page, state.projectId, `colnav-backlog-${Date.now()}`);
        await navigateToTask(page, backlogTask);
        await page.waitForSelector('.column-chat .chat-messages', { timeout: 15000 });
        expect(await page.locator('.column-diff button.diff-file-header').count()).toBe(0);

        await page.evaluate(() => (document.querySelector('.column-chat .chat-messages') as HTMLElement).focus());
        await page.keyboard.press('ArrowRight');
        const info = await activeInfo(page);
        expect(info.isDiffColumn).toBeTruthy();
        expect(info.tag).toBe('DIV');

        // Wrap from the empty diff column back to the Board.
        await page.keyboard.press('ArrowRight');
        expect((await activeInfo(page)).isOverviewCard).toBeTruthy();
    });
});
