import { test, expect } from '@playwright/test';
import { getProjectId, selectProject } from './helpers';

test.describe.configure({ mode: 'serial' });

test.describe('Find in Files', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
        // The row of global-setup's project, addressed by id: `.project-item` + `.first()` is
        // whichever project some other worker created last.
        await selectProject(page, await getProjectId(page));
        await page.waitForSelector('.card, .empty-state, .file-tree', { timeout: 10000 });

        // Open the Explorer view
        await page.locator('.explorer-btn').click();
        await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });
    });

    async function pressFindShortcut(page: import('@playwright/test').Page) {
        // The explorer's handler asks for `e.key === 'F'` (`explorer-core.svelte.ts`), so the modifiers are
        // held across the key press: `press('Control+Shift+f')` delivers a lowercase `key` and nothing opens.
        await page.keyboard.down('Control');
        await page.keyboard.down('Shift');
        await page.keyboard.press('KeyF');
        await page.keyboard.up('Shift');
        await page.keyboard.up('Control');
    }

    // One gesture pair, one test: the shortcut opens the panel and closes it again, and Escape closes it.
    // Split in two, each half re-drove the whole explorer to assert half a toggle.
    test('Ctrl+Shift+F opens the find panel and closes it again, and Escape closes it', async ({ page }) => {
        await pressFindShortcut(page);
        const panel = page.locator('.file-search-panel');
        await expect(panel).toBeVisible();

        await pressFindShortcut(page);
        await expect(panel).toHaveCount(0);

        await pressFindShortcut(page);
        await expect(panel).toBeVisible();
        await page.keyboard.press('Escape');
        await expect(panel).toHaveCount(0);
    });
});

test.describe('Sidebar persistence', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
    });

    // What survives the reload is the point: the collapsed flag is written to the client store at boot and
    // read back, so the class is asked for after a real navigation rather than after a sleep.
    test('the collapsed sidebar stays collapsed across a reload', async ({ page }) => {
        const toggleBtn = page.locator('.desktop-collapse-btn');
        await expect(toggleBtn).toBeVisible();

        await toggleBtn.click();
        await expect(page.locator('#sidebar')).toHaveClass(/collapsed/);

        await page.reload();
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
        await expect(page.locator('#sidebar')).toHaveClass(/collapsed/);

        await toggleBtn.click();
        await expect(page.locator('#sidebar')).not.toHaveClass(/collapsed/);

        await page.reload();
        await page.waitForSelector('.project-item, .empty-state', { timeout: 10000 });
        await expect(page.locator('#sidebar')).not.toHaveClass(/collapsed/);
    });
});
