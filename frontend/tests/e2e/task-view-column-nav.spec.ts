import { test, expect, type Page } from '@playwright/test';
import { execSync } from 'child_process';
import {
  getProjectId, createTaskViaApi, navigateToTask, csrfFetch, isolatedProject, gotoProject,
} from './helpers';
// Each test file gets its own project, because it starts a turn: see isolatedProject() in helpers.ts.
isolatedProject();


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
            await gotoProject(page);
            await page.waitForSelector('.board .column', { timeout: 10000 });
            state! = await setupFixture(page);
        } else {
            await gotoProject(page);
        }
        await navigateToTask(page, state.taskId);
        await waitForStableHeaders(page);
    });

    test('Right cycles Board -> Chat -> Diff -> Board; Left cycles back', async ({ page }) => {
        const firstCard = page.locator('.column-context .task-wrapper > .card').first();
        await firstCard.focus();

        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true });

        // Plain arrows edit text in the chat input; Ctrl+Alt+Arrow leaves it.
        await page.keyboard.press('Control+Alt+ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isDiffHeader: true, inDiff: true });

        // Wrap: Right from Diff -> Board
        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isOverviewCard: true, inBoard: true });

        // Left: Board -> Diff -> Chat -> Board (cyclic)
        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isDiffHeader: true });

        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true });

        await page.keyboard.press('Control+Alt+ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isOverviewCard: true, inBoard: true });
    });

    test('Left/Right in the chat input is native text editing (no column switch)', async ({ page }) => {
        const input = page.locator('.column-chat textarea');
        await input.focus();
        await input.fill('hello world'); // caret at end (11)

        await page.keyboard.press('ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true, inChat: true });
        expect(await input.evaluate(el => (el as HTMLTextAreaElement).selectionStart)).toBe(10);

        await page.keyboard.press('ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true, inChat: true });
        expect(await input.evaluate(el => (el as HTMLTextAreaElement).selectionStart)).toBe(11);
    });

    test('plain Ctrl+Left/Right in the chat input is native word jump (no column switch)', async ({ page }) => {
        const input = page.locator('.column-chat textarea');
        await input.focus();
        await input.fill('hello world'); // caret at end (11)

        await page.keyboard.press('Control+ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true, inChat: true });
        expect(await input.evaluate(el => (el as HTMLTextAreaElement).selectionStart)).toBe(6);

        await page.keyboard.press('Control+ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true, inChat: true });
        expect(await input.evaluate(el => (el as HTMLTextAreaElement).selectionStart)).toBe(11);
    });

    test('Ctrl+Alt+Left/Right in the chat input switch columns', async ({ page }) => {
        const input = page.locator('.column-chat textarea');
        await input.focus();
        await input.fill('hello world');

        await page.keyboard.press('Control+Alt+ArrowRight');
        expect(await activeInfo(page)).toMatchObject({ isDiffHeader: true, inDiff: true });

        // Back: Ctrl+Alt+Left from Diff lands on the chat input again.
        await page.keyboard.press('Control+Alt+ArrowLeft');
        expect(await activeInfo(page)).toMatchObject({ isChatInput: true });
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
            // Wait for the turn to be over instead of sleeping: a response that lands later
            // than a fixed delay auto-scrolls the chat mid-measurement, and a page-scroll
            // assertion then reads the bottom of the list instead of one page down.
            await expect(page.locator('.column-chat .btn-send')).toBeVisible({ timeout: 30000 });
        }
        await page.waitForFunction(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            return el.scrollHeight > 400;
        }, undefined, { timeout: 15000 });

        const scrollTop = () => page.evaluate(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            return el.scrollTop;
        });
        // Direction, not distance: the step is the list's own (80px per ArrowUp/Down, 0.8x its
        // tracked clientHeight for PageUp/Down), and an injected maxHeight measures the app
        // against a height it has not re-measured yet. Down goes down, Up comes back.
        // Settle before zeroing too: if the list auto-scrolls while we measure, the reset loses
        // and scrollTop never reads 0 — which looks like "the chat will not scroll".
        const settledScrollTop = async () => {
            let prev = -1;
            for (let i = 0; i < 25; i++) {
                const now = await scrollTop();
                if (now === prev) return now;
                prev = now;
                await page.waitForTimeout(200);
            }
            return prev;
        };
        await settledScrollTop();
        await page.evaluate(() => (document.querySelector('.column-chat .chat-messages') as HTMLElement).focus());
        await page.evaluate(() => {
            const el = document.querySelector('.column-chat .chat-messages') as HTMLElement;
            el.scrollTop = 0;
        });
        await expect.poll(scrollTop, { timeout: 10000 }).toBe(0);

        // A position sampled mid-settle is not a position: the virtualised list keeps
        // re-measuring (content-visibility) and can auto-scroll to the bottom a beat after the
        // key, which then swamps the 80px ArrowUp step and looks like "Up does nothing".
        await page.keyboard.press('ArrowDown');
        await expect
            .poll(async () => (await scrollTop()) > 0, { timeout: 5000, message: 'ArrowDown scrolls the chat down' })
            .toBe(true);
        const scrolledTo = await settledScrollTop();
        expect(scrolledTo, 'still below the top after settling').toBeGreaterThan(0);

        await page.keyboard.press('ArrowUp');
        await expect
            .poll(async () => (await scrollTop()) < scrolledTo, { timeout: 5000, message: 'ArrowUp scrolls the chat back up' })
            .toBe(true);

        // Down in the chat INPUT must not scroll (native text editing).
        // Captured only once the scroller is QUITE (see above): a position sampled mid-settle
        // moves on its own, which reads exactly like the key having scrolled the list.
        await input.focus();
        const inputTop = await settledScrollTop();
        await page.keyboard.press('ArrowDown');
        await page.waitForTimeout(400);
        expect(await scrollTop(), 'arrows edit text in the input, they do not scroll the chat').toBe(inputTop);
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
