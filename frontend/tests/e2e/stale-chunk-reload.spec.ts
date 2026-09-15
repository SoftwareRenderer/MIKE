import { test, expect } from '@playwright/test';

// A document left open across an update imports chunk URLs the new build deleted, so the panel the
// user clicked never appears. The click is not broken, so only a notice can carry the news.

// The precached copy would answer for the chunk and no request would reach the network.
test.use({ serviceWorkers: 'block' });

const TERMINAL_BUTTON = '[aria-label="Open terminal"]';
const TERMINAL_PANEL = '.terminal-panel';
const TOAST = '.toast';

test.describe('A lazy part of the app the browser cannot fetch', () => {
    test('says the page needs a reload, and the next click still reaches the panel', async ({ page }) => {
        // One CSS chunk of the terminal's import graph is refused; everything else is normal. A
        // refused stylesheet rejects the import without poisoning the module map, so a retry works.
        let refuse = true;
        await page.route('**/assets/**', async route => {
            const isTerminalCss = /\/TerminalPanel-[^/]+\.css$/.test(new URL(route.request().url()).pathname);
            if (isTerminalCss && refuse) await route.abort('failed');
            else await route.fallback();
        });

        await page.goto('/');
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

        await page.locator(TERMINAL_BUTTON).click();

        const toast = page.locator(TOAST);
        await expect(toast).toBeVisible({ timeout: 15000 });
        await expect(toast).toContainText(/reload/i);
        await expect(page.locator(TERMINAL_PANEL)).toHaveCount(0);

        // No latch: `terminalLoading` left true would make this click return before importing.
        refuse = false;
        await page.locator(TERMINAL_BUTTON).click();
        await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 15000 });
    });
});
