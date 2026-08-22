import { test, expect, type Page } from '@playwright/test';

/**
 * Settings tabs on mobile — at phone widths (≤768px) the five Settings tabs
 * don't fit one row, so the tab row must be horizontally scrollable and every
 * tab reachable. On desktop (fixed 720px modal) all tabs fit — no scroll.
 *
 * The suite only opens the modal and switches tabs (in-memory state), so it is
 * safe on the shared main server in the parallel chromium project.
 */

async function openSettings(page: Page, isMobile: boolean) {
    await page.goto('/');
    if (isMobile) {
        // Mobile: the top-bar settings button is desktop-only — open via the
        // hamburger drawer.
        await page.waitForSelector('.hamburger', { state: 'visible', timeout: 10000 });
        await page.click('.hamburger');
        await page.click('.sidebar-menu-item[aria-label="Settings"]');
    } else {
        await page.waitForSelector('.icon-btn[aria-label="Settings"]', { state: 'visible', timeout: 10000 });
        await page.click('.icon-btn[aria-label="Settings"]');
    }
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
}

test.describe('Settings tabs — mobile horizontal scroll', () => {
    test('tab row scrolls horizontally and every tab is reachable on mobile', async ({ page }) => {
        await page.setViewportSize({ width: 375, height: 667 });
        await openSettings(page, true);

        const tabs = page.locator('.header-tabs');
        const metrics = await tabs.evaluate(el => ({
            scrollWidth: el.scrollWidth,
            clientWidth: el.clientWidth,
        }));
        // The five tabs don't fit a phone-width row.
        expect(metrics.scrollWidth).toBeGreaterThan(metrics.clientWidth);

        // The last tab starts off-screen.
        const aboutTab = page.locator('.header-tabs .tab-btn', { hasText: 'About' });
        await expect(aboutTab).not.toBeInViewport();

        // Scrolling the row to the end reveals it.
        await tabs.evaluate(el => { el.scrollLeft = el.scrollWidth; });
        await expect(aboutTab).toBeInViewport();

        // And it is clickable — switches to the About tab.
        await aboutTab.click();
        await expect(page.locator('.header-tabs .tab-btn.active')).toContainText('About');
        await expect(page.locator('.about-version')).toBeVisible();

        // Close without saving.
        await page.click('.modal .close-btn');
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });

    test('tab row is not a scroll container on desktop', async ({ page }) => {
        await openSettings(page, false);

        const tabs = page.locator('.header-tabs');
        // The horizontal-scroll rule is mobile-scoped — desktop keeps the
        // default (non-scrolling) overflow.
        expect(await tabs.evaluate(el => getComputedStyle(el).overflowX)).toBe('visible');

        // All five tabs remain on screen at the fixed 720px modal width.
        for (const name of ['Providers', 'Task Defaults', 'Preferences', 'Extensions', 'About']) {
            await expect(page.locator('.header-tabs .tab-btn', { hasText: name })).toBeInViewport();
        }

        await page.click('.modal .close-btn');
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});
