import { test, expect, devices } from '@playwright/test';

const CONCURRENCY_TIP = 'Max tasks running concurrently on this provider; also caps in-flight LLM requests to this number.';
const MAX_RPS_TIP = 'Maximum new requests started per second to this provider. 0 = unlimited.';

/**
 * Provider option help tooltips on touch devices. Native `title` tooltips
 * never surface on tap, so in a touch context (`hover: none`) the "?" icons
 * next to "Concurrency" and "Max requests/sec" must show their message as a
 * press-to-show tooltip bubble instead.
 *
 * Only in-memory settings state is touched (a fresh provider card is added
 * and discarded, never saved), so the suite is safe on the shared main
 * server in the parallel chromium project.
 */
test.describe.serial('Provider option help tooltips (touch)', () => {
    test('pressing the Concurrency and Max requests/sec help icons shows the message as a tooltip', async ({ browser }) => {
        const context = await browser.newContext({
            ...devices['iPhone 13'],
            storageState: '/tmp/kanban-test-storage-state.json',
        });
        const page = await context.newPage();
        try {
            await page.goto('/');

            // Mobile: the top-bar settings button is desktop-only — open via
            // the hamburger drawer.
            await page.waitForSelector('.hamburger', { state: 'visible', timeout: 10000 });
            await page.click('.hamburger');
            await page.click('.sidebar-menu-item[aria-label="Settings"]');
            await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

            // The Providers tab is active by default. A fresh (unsaved)
            // provider card is expanded by default, so the Concurrency /
            // Max requests/sec rows exist without persisting anything.
            await page.getByRole('button', { name: '+ Add Provider' }).click();
            const card = page.locator('.provider-card').last();
            await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });

            // On touch devices the help icon is a pressable button.
            const concurrencyHelp = card.getByRole('button', { name: 'Concurrency help' });
            await expect(concurrencyHelp).toBeVisible({ timeout: 5000 });

            // Press the Concurrency help icon — the message appears as a tooltip.
            await concurrencyHelp.click();
            const tooltip = page.getByRole('tooltip');
            await expect(tooltip).toBeVisible({ timeout: 5000 });
            await expect(tooltip).toHaveText(CONCURRENCY_TIP);

            // Pressing again dismisses it.
            await concurrencyHelp.click();
            await expect(tooltip).toBeHidden({ timeout: 5000 });

            // Same behavior for Max requests/sec.
            const rpsHelp = card.getByRole('button', { name: 'Max requests/sec help' });
            await rpsHelp.click();
            await expect(tooltip).toBeVisible({ timeout: 5000 });
            await expect(tooltip).toHaveText(MAX_RPS_TIP);

            // Tapping outside dismisses it.
            await page.locator('#modal-title').click();
            await expect(tooltip).toBeHidden({ timeout: 5000 });

            // Close settings — the unsaved provider is discarded.
            await page.click('.modal .header-actions .close-btn');
            const discardBtn = page.getByRole('button', { name: 'Discard' });
            if (await discardBtn.isVisible().catch(() => false)) {
                await discardBtn.click();
            }
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
        } finally {
            await context.close();
        }
    });

    test('hover-capable devices keep the native title tooltip (no button)', async ({ page }) => {
        // The default chromium context is hover-capable: the icons stay
        // title-bearing spans, not buttons.
        await page.goto('/');
        await page.waitForSelector('.icon-btn[aria-label="Settings"]', { state: 'visible', timeout: 10000 });
        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        await page.getByRole('button', { name: '+ Add Provider' }).click();
        const card = page.locator('.provider-card').last();
        await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        const concurrencyIcon = card.locator('.option-label-group', { hasText: 'Concurrency' }).locator('.help-icon');
        await expect(concurrencyIcon).toHaveAttribute('title', CONCURRENCY_TIP);
        await expect(card.getByRole('button', { name: 'Concurrency help' })).toHaveCount(0);

        const rpsIcon = card.locator('.option-label-group', { hasText: 'Max requests/sec' }).locator('.help-icon');
        await expect(rpsIcon).toHaveAttribute('title', MAX_RPS_TIP);
        await expect(card.getByRole('button', { name: 'Max requests/sec help' })).toHaveCount(0);

        // Close settings — the unsaved provider is discarded.
        await page.click('.modal .header-actions .close-btn');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});
