import { test, expect } from '@playwright/test';

// A slow session answer is not an unreachable server, so the offline mirror the 2 s timer offers has
// to be retractable. Before the fix the screen stayed up and two `/api/stream` sockets were open.

const OFFLINE_SCREEN = '.offline-screen';
const OFFLINE_BUTTON = '.offline-btn';
const SLOW_SESSION_MS = 6000; // past the 2 s offer, with room for the click in between

/** Counts live `/api/stream` sockets: a socket the app no longer holds is still a socket. */
function countLiveStreamSockets(page: import('@playwright/test').Page) {
    let live = 0;
    page.on('websocket', ws => {
        if (!ws.url().includes('/api/stream')) return;
        live++;
        ws.on('close', () => { live--; });
    });
    return () => live;
}

test.describe('Boot with a session check that is slow rather than gone', () => {
    test('the mirror offer comes down by itself and leaves one stream', async ({ page }) => {
        // The first visit is what puts a projects list in the mirror, which is the only reason the
        // timer has anything to offer.
        await page.goto('/');
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

        const liveSockets = countLiveStreamSockets(page);
        await page.route('**/api/auth/session*', async route => {
            await new Promise(resolve => setTimeout(resolve, SLOW_SESSION_MS));
            await route.continue();
        });
        await page.reload();

        const offline = page.locator(OFFLINE_SCREEN);
        await expect(offline).toBeVisible({ timeout: 10000 });
        await expect(offline).toContainText('Taking longer than usual');
        await expect(offline).not.toContainText('Server unreachable');

        // The way this was hit: the user takes the mirror, and the session answer arrives while the
        // cached board is being painted.
        await page.locator(OFFLINE_BUTTON).click();

        await expect(page.locator('.card').first()).toBeVisible({ timeout: 30000 });
        await expect(offline).toHaveCount(0);

        await expect
            .poll(() => liveSockets(), { timeout: 15000, message: 'a second /api/stream socket was left open' })
            .toBe(1);
    });

    test('with nothing in the mirror, a slow answer is a wait and not a sign-in wall', async ({ page }) => {
        // A fresh context has no mirror, so the timer has nothing to offer and only the wait shows.
        await page.route('**/api/auth/session*', async route => {
            await new Promise(resolve => setTimeout(resolve, 5000));
            await route.continue();
        });

        await page.goto('/');
        await expect(page.locator(OFFLINE_SCREEN)).toHaveCount(0);
        await expect(page.locator('.loading-slow')).toBeVisible({ timeout: 10000 });

        await expect(page.locator('.card').first()).toBeVisible({ timeout: 30000 });
        await expect(page.locator('.loading-slow')).toHaveCount(0);
    });
});
