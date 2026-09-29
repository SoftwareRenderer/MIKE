import { test, expect, type Page } from '@playwright/test';

// The reconnecting banner and the wake-up refresh. `page.route` cannot intercept WebSockets, so the
// stream is blocked by wrapping its constructor, as in `reconnect-refresh.spec.ts`.

const BANNER = '.reconnecting-banner';
/** App.svelte's one announced sentence: the transitions, never the ticking numbers beside them. */
const ANNOUNCEMENT = '.sr-only[role="status"]';

/** Keeps a handle on the live `/api/stream` socket so the spec can drop it and refuse handshakes. */
async function watchStream(page: Page): Promise<void> {
    await page.addInitScript(() => {
        const w = window as any;
        w.__streamSockets = [];
        w.__streamAttempts = 0;
        w.__blockStream = false;
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

test.describe('Reconnecting banner and tab wake-up', () => {
    test('a drop announces the change, not the countdown', async ({ page }) => {
        await watchStream(page);
        await page.goto('/');
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        await page.waitForFunction(() => (window as any).__streamSockets.length > 0, null, { timeout: 15000 });

        await dropStream(page);
        await expect(page.locator(BANNER)).toBeVisible({ timeout: 15000 });
        // Hold it down: the app would otherwise reconnect before the sampling loop finishes.
        await page.evaluate(() => { (window as any).__blockStream = true; });

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
        await page.goto('/');
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

        await page.goto('/');
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

        expect(consoleErrors.filter(e => !e.includes('Navigation duplicated'))).toHaveLength(0);
        await expect(page.locator('.card').first()).toBeVisible();
    });
});
