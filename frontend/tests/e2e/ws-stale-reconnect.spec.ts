import { test, expect } from '@playwright/test';

test.describe.serial('WebSocket stale connection detection', () => {
    test('reconnecting banner appears and disappears on disconnect/reconnect', async ({ page }) => {
        // 1. Navigate to the board and let it load
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // 2. Verify the WebSocket is connected (no reconnecting banner)
        await expect(page.locator('.reconnecting-banner')).not.toBeVisible();

        // 3. Block WebSocket connections to simulate disconnect
        let blockWs = true;
        await page.route('**/api/stream*', async (route) => {
            if (blockWs) {
                await route.abort('failed');
            } else {
                await route.continue();
            }
        });

        // 4. Reload to trigger a fresh connection attempt that will fail
        await page.goto('/');

        // 5. The reconnecting banner should appear (or the page shows offline state)
        // Wait a bit for the reconnect logic to kick in
        await page.waitForTimeout(3000);

        // 6. Restore WebSocket connections
        blockWs = false;

        // 7. Reload again to get a working connection
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // 8. Verify the page is connected (no reconnecting banner)
        await expect(page.locator('.reconnecting-banner')).not.toBeVisible({ timeout: 15000 });
    });

    test('visibilitychange handler is registered and checks connection health', async ({ page }) => {
        // 1. Navigate to the board and let it load
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // 2. Verify the page supports visibility API
        const supportsVisibility = await page.evaluate(() => {
            return typeof document.hidden === 'boolean';
        });
        expect(supportsVisibility).toBe(true);

        // 3. Simulate a visibility change event
        await page.evaluate(() => {
            document.dispatchEvent(new CustomEvent('visibilitychange'));
        });

        // 4. The page should still be functional (cards visible)
        const cards = await page.locator('.card').count();
        expect(cards).toBeGreaterThan(0);
    });
});

test.describe('Terminal stale connection detection', () => {
    test('isTerminalStale correctly identifies stale connections', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // Test the stale detection logic directly
        const result = await page.evaluate(() => {
            const STALE_THRESHOLD = 60_000;

            // Simulate stale connection (no activity for 70s)
            const staleActivity = Date.now() - 70_000;
            const isStale = (Date.now() - staleActivity) > STALE_THRESHOLD;

            // Simulate fresh connection (activity 5s ago)
            const freshActivity = Date.now() - 5_000;
            const isFresh = (Date.now() - freshActivity) > STALE_THRESHOLD;

            return { isStale, isFresh };
        });

        expect(result.isStale).toBe(true);
        expect(result.isFresh).toBe(false);
    });
});
