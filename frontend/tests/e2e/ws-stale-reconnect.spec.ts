import { test, expect, type Page } from '@playwright/test';
import {
    clearTasksAfterEach, createTaskViaApi, ensureTaskStarted, getProjectId, gotoProject,
    isolatedProject, navigateToTask, serverApi, waitForSendButton,
} from './helpers';

// The reconnecting banner, the wake-up refresh, and the screen that comes back holding the server's
// answer. `page.route` cannot intercept WebSockets, so the stream is blocked by wrapping its
// constructor; the block flag is seeded from localStorage, so a reload comes back still offline.

const BANNER = '.reconnecting-banner';
/** App.svelte's one announced sentence: the transitions, never the ticking numbers beside them. */
const ANNOUNCEMENT = '.sr-only[role="status"]';

// Own project: these tests go away and come back, and expect the board to carry what was written while
// this window was not reading it. On a board another worker is moving cards on, a missing card is
// indistinguishable from a lost update. See isolatedProject() in helpers.ts.
isolatedProject();
clearTasksAfterEach();

/** Keeps a handle on the live `/api/stream` socket so the spec can drop it and refuse handshakes. */
async function watchStream(page: Page): Promise<void> {
    await page.addInitScript(() => {
        const w = window as any;
        w.__streamSockets = [];
        w.__streamAttempts = 0;
        w.__blockStream = localStorage.getItem('__ws_blocked') === '1';
        const RealWebSocket = window.WebSocket;
        window.WebSocket = class extends RealWebSocket {
            constructor(url: string | URL, protocols?: string | string[]) {
                super(url, protocols);
                if (!String(url).includes('/api/stream')) return;
                w.__streamAttempts++;
                if (w.__blockStream) { this.close(); return; }
                w.__streamSockets.push(this);
            }
        };
    });
}

const streamAttempts = (page: Page) => page.evaluate(() => (window as any).__streamAttempts as number);

/** The seconds the banner is counting down, or 0 when it asks for no wait. */
async function countdownSeconds(page: Page): Promise<number> {
    const match = (await page.locator(BANNER).innerText()).match(/Reconnecting in (\d+)s/);
    return match ? Number(match[1]) : 0;
}

async function dropStream(page: Page): Promise<void> {
    const dropped = await page.evaluate(() => {
        const live = ((window as any).__streamSockets as WebSocket[])
            .filter(socket => socket.readyState === WebSocket.OPEN);
        for (const socket of live) socket.close();
        return live.length;
    });
    expect(dropped, 'the app had no open /api/stream socket to drop').toBeGreaterThan(0);
}

/** Be away: the flag outlives the reload, so every handshake from here is refused while HTTP answers. */
async function enterOfflineState(page: Page): Promise<void> {
    await page.evaluate(() => localStorage.setItem('__ws_blocked', '1'));
    await page.reload();
    await expect(page.locator(BANNER)).toBeVisible({ timeout: 15000 });
}

/** Handshakes answer again; the client's own ladder takes it from there. */
async function restoreConnection(page: Page): Promise<void> {
    await page.evaluate(() => {
        (window as any).__blockStream = false;
        localStorage.removeItem('__ws_blocked');
    });
}

test.describe('Reconnecting banner and tab wake-up', () => {
    test('a drop announces the change, not the countdown', async ({ page }) => {
        await watchStream(page);
        await gotoProject(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        await page.waitForFunction(() => (window as any).__streamSockets.length > 0, null, { timeout: 15000 });

        // Hold the outage down first: a socket back inside the grace period shows no banner at all.
        await page.evaluate(() => { (window as any).__blockStream = true; });
        await dropStream(page);
        await expect(page.locator(BANNER)).toBeVisible({ timeout: 15000 });

        // The countdown and the data age change every second, so only the transition is announced:
        // one sentence, no ticking digits.
        const spoken: string[] = [];
        for (let i = 0; i < 10; i++) {
            spoken.push((await page.locator(ANNOUNCEMENT).innerText()).trim());
            await page.waitForTimeout(500);
        }
        const announced = [...new Set(spoken.filter(text => text !== ''))];
        expect(announced, `the live region churned: ${JSON.stringify(announced)}`).toHaveLength(1);
        expect(announced[0]).toMatch(/Reconnecting/);
        expect(announced[0]).not.toMatch(/\d/);

        // The way back is announced too, and the banner button takes it.
        await page.evaluate(() => { (window as any).__blockStream = false; });
        await page.locator('.reconnecting-btn').click();
        await expect(page.locator(ANNOUNCEMENT)).toHaveText(/Connected again/, { timeout: 30000 });
        await expect(page.locator(BANNER)).toHaveCount(0);
    });

    test('the window coming back cuts the countdown short', async ({ page }) => {
        // The ladder is waited out on purpose: the rung being measured has to arrive on its own.
        test.setTimeout(90000);
        await watchStream(page);
        await gotoProject(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        await page.waitForFunction(() => (window as any).__streamSockets.length > 0, null, { timeout: 15000 });

        // Handshakes stop answering and the live socket goes with them, so the retry ladder starts.
        await page.evaluate(() => { (window as any).__blockStream = true; });
        await dropStream(page);
        await expect(page.locator(BANNER)).toBeVisible({ timeout: 15000 });

        // Wait for a rung long enough to tell an interrupted wait from one that ran out.
        await expect
            .poll(() => countdownSeconds(page), { timeout: 20000, message: 'the countdown never climbed' })
            .toBeGreaterThanOrEqual(4);

        const before = await streamAttempts(page);
        await page.evaluate(() => { (window as any).__blockStream = false; });
        // The return signal, dispatched as the browser dispatches it — at `document`, bubbling to
        // the `window` listeners the app installed.
        await page.evaluate(() => {
            document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
        });

        // The attempt goes out on the signal, not when the countdown runs out.
        await expect
            .poll(() => streamAttempts(page), { timeout: 2000, message: 'the return waited out the countdown' })
            .toBeGreaterThan(before);
        await expect(page.locator(BANNER)).toHaveCount(0, { timeout: 15000 });
    });

    test('the tab becoming visible refreshes the board', async ({ page }) => {
        const boardFetches: string[] = [];
        await page.route('**/api/projects/*/board*', async route => {
            boardFetches.push(route.request().url());
            await route.fallback();
        });

        const consoleErrors: string[] = [];
        page.on('console', msg => { if (msg.type() === 'error') consoleErrors.push(msg.text()); });

        await gotoProject(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

        // The wake-up path. The browser fires this at `document`, where it bubbles as far as
        // `window`; a plain `new Event(...)` does not bubble and the handler never runs.
        const before = boardFetches.length;
        await page.evaluate(() => {
            document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
        });

        await expect
            .poll(() => boardFetches.length, { timeout: 15000, message: 'waking the tab issued no board fetch' })
            .toBeGreaterThan(before);

        // The two sentences the lane always prints, neither of which belongs to the wake-up path: the
        // app's report of a replaced address, and Chromium refusing `/sw.js` — each lane server signs with
        // a CA its own data directory created, and `ignoreHTTPSErrors` does not reach a service-worker
        // script fetch. The app falls back to the asset identity (`docs/frontend-updates.md`), and the
        // suite is written against a lane with no worker in it (`ui-navigation.spec.ts`).
        const laneNoise = (e: string) => e.includes('Navigation duplicated')
            || e.includes('An SSL certificate error occurred when fetching the script');
        expect(consoleErrors.filter(e => !laneNoise(e))).toHaveLength(0);
        await expect(page.locator('.card').first()).toBeVisible();
    });

    test('the board holds the card the server wrote while this window was away', async ({ page, baseURL }) => {
        test.setTimeout(90000);
        await watchStream(page);
        await gotoProject(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        const projectId = await getProjectId(page);
        const base = baseURL || 'https://localhost:3000';
        const unique = `away-board-${Date.now()}`;

        await enterOfflineState(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

        const created = await serverApi(base, `/api/projects/${projectId}/tasks`, {
            method: 'POST', body: { prompt: unique },
        });
        expect(created.ok, `task creation failed: ${JSON.stringify(created.data)}`).toBe(true);
        // Nothing has told this window about it, and the board is not re-read while the socket is down.
        await expect(page.locator('.card', { hasText: unique })).toHaveCount(0);

        await restoreConnection(page);
        await expect(page.locator('.card', { hasText: unique })).toBeVisible({ timeout: 45000 });
        await expect(page.locator(BANNER)).toHaveCount(0, { timeout: 15000 });
    });

    test('the chat holds the message the server wrote while this window was away', async ({ page, baseURL }) => {
        test.setTimeout(120000);
        await watchStream(page);
        await gotoProject(page);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        const projectId = await getProjectId(page);
        const base = baseURL || 'https://localhost:3000';

        const taskId = await createTaskViaApi(page, projectId, `away-chat ${Date.now()}`);
        await navigateToTask(page, taskId, projectId);
        expect(await ensureTaskStarted(page, projectId, taskId)).toBe(true);
        await expect(page.locator('.chat-message.assistant').last()).toBeVisible({ timeout: 60000 });
        await waitForSendButton(page);
        const rowsBefore = await page.locator('.chat-message').count();

        // Chat loads its history over HTTP, so the away window still shows the turn that finished.
        await enterOfflineState(page);
        await expect(page.locator('.chat-message.assistant').last()).toBeVisible({ timeout: 15000 });

        const marker = `away-marker-${Date.now()}`;
        const sent = await serverApi(base, `/api/projects/${projectId}/tasks/${taskId}/send`, {
            method: 'POST', body: { content: marker },
        });
        expect(sent.ok, `send failed: ${JSON.stringify(sent.data)}`).toBe(true);

        // The turn is over on the server before the return, so what follows is a screen that did not
        // learn, not a turn still queued.
        await expect(async () => {
            const res = await serverApi(base, `/api/projects/${projectId}/tasks/${taskId}/messages`);
            const rows: any[] = Array.isArray(res.data) ? res.data : [];
            expect(rows.some(m => m.role === 'user' && m.content === marker)).toBe(true);
            expect(rows.some(m => m.role === 'assistant')).toBe(true);
        }).toPass({ timeout: 30000 });

        await restoreConnection(page);
        await expect(page.locator('.chat-message.user', { hasText: marker })).toBeVisible({ timeout: 45000 });
        await expect
            .poll(() => page.locator('.chat-message').count(), { timeout: 15000 })
            .toBeGreaterThan(rowsBefore);
        await expect(page.locator(BANNER)).toHaveCount(0, { timeout: 15000 });
    });
});
