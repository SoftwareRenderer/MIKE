import { test, expect } from '@playwright/test';

// A chunk URL the open document asks for can be gone; the click that hits one reloads the page.

// The precached copy would answer for the chunk and no request would reach the network.
test.use({ serviceWorkers: 'block' });

const TERMINAL_BUTTON = '[aria-label="Open terminal"]';
const TERMINAL_PANEL = '.terminal-panel';

test.describe('A lazy part of the app the browser cannot fetch', () => {
    // A reload wipes the page, so what the first document showed lives only where this wrote it.
    const initRecorder = () => {
        const record = (line: string) => {
            const seen = sessionStorage.getItem('e2e-toasts');
            sessionStorage.setItem('e2e-toasts', seen ? `${seen}\n${line}` : line);
        };
        sessionStorage.setItem('e2e-boots', String(Number(sessionStorage.getItem('e2e-boots') ?? 0) + 1));
        const watch = () => {
            new MutationObserver(() => {
                const el = document.querySelector('.toast');
                const text = el?.textContent?.trim();
                if (el && text) record(`${el.className}::${text}`);
            }).observe(document.body, { childList: true, subtree: true });
        };
        if (document.body) watch();
        else document.addEventListener('DOMContentLoaded', watch);
    };

    test('reloads the page, and the click after the reload reaches the panel', async ({ page }) => {
        // Refused once: a stylesheet failure rejects the import without poisoning the module map.
        let refuse = true;
        await page.route('**/assets/**', async route => {
            const isTerminalCss = /\/TerminalPanel-[^/]+\.css$/.test(new URL(route.request().url()).pathname);
            if (isTerminalCss && refuse) {
                refuse = false;
                await route.abort('failed');
            } else await route.fallback();
        });
        await page.addInitScript(initRecorder);

        await page.goto('/');
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });
        expect(await page.evaluate(() => Number(sessionStorage.getItem('e2e-boots')))).toBe(1);

        await page.locator(TERMINAL_BUTTON).click();

        // The page is gone: a new document booted in its place.
        await expect
            .poll(() => page.evaluate(() => Number(sessionStorage.getItem('e2e-boots'))), { timeout: 15000 })
            .toBeGreaterThanOrEqual(2);
        await expect(page.locator('.card').first()).toBeVisible({ timeout: 15000 });

        // It said what it was doing, and never asked the person to reload by hand.
        const toasts = await page.evaluate(() => sessionStorage.getItem('e2e-toasts') ?? '');
        expect(toasts).toContain('Updating the app ...');
        expect(toasts).not.toMatch(/\berror\b/);

        // The reload is what the panel needed; nothing latched it shut.
        await page.locator(TERMINAL_BUTTON).click();
        await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 15000 });
    });
});
