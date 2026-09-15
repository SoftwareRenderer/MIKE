import { test, expect, type Page } from '@playwright/test';
import { getProjectId, createTaskViaApi, ensureTaskStarted, navigateToTask } from './helpers';

// Open/close-terminal keyboard shortcut (default Ctrl+backtick, Settings →
// Preferences). Toggles the panel on every page with a terminal button: board
// (TopBar), task detail (TaskHeader), project explorer, task explorer — and is
// inactive while another modal is open. The panel open itself is exercised by
// ui-navigation.spec.ts.

const TERMINAL_PANEL = '.terminal-panel';
const TERMINAL_CLOSE = '.terminal-panel .close-btn';
const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const PREFERENCES_TAB = 'button.tab-btn:has-text("Preferences")';

async function pressOpenTerminal(page: Page) {
    await page.keyboard.press('Control+`');
}

async function expectTerminalVisible(page: Page) {
    await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 10000 });
}

async function closeTerminal(page: Page) {
    await page.locator(TERMINAL_CLOSE).click();
    await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });
}

test.describe.serial('Open-terminal keyboard shortcut', () => {
    test('board: Ctrl+` opens the terminal panel, pressing again closes it', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const terminalButton = page.locator('[aria-label="Open terminal"]');
        await expect(terminalButton).toBeVisible();
        await expect(terminalButton).toHaveAttribute('title', 'Open terminal (Ctrl+`)');

        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible();
        await pressOpenTerminal(page);
        await expectTerminalVisible(page);
        // Close button tooltip shows the shortcut (the combo toggles the panel).
        await expect(page.locator(TERMINAL_CLOSE)).toHaveAttribute('title', 'Close terminal (Ctrl+`)');

        // Toggle: the same combo closes the panel (even with xterm focused).
        await pressOpenTerminal(page);
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });
    });

    test('task detail: Ctrl+` opens the terminal panel', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `terminal-shortcut-${Date.now()}`);
        const started = await ensureTaskStarted(page, projectId, taskId);
        test.skip(!started, 'Could not start task');

        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible();

        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible();
        await pressOpenTerminal(page);
        await expectTerminalVisible(page);
        // Close via the button (the toggle close is covered on the board test).
        await closeTerminal(page);
    });

    test('project explorer: Ctrl+` opens the terminal panel', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const projectId = await getProjectId(page);
        await page.goto(`/?project=${projectId}&view=explorer`);

        const terminalButton = page.locator('.terminal-btn[aria-label="Open terminal"]');
        await expect(terminalButton).toBeVisible();

        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible();
        await pressOpenTerminal(page);
        await expectTerminalVisible(page);
        await pressOpenTerminal(page);
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });
    });

    test('task explorer: Ctrl+` opens the terminal panel', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `terminal-shortcut-explorer-${Date.now()}`);
        const started = await ensureTaskStarted(page, projectId, taskId);
        test.skip(!started, 'Could not start task');

        await navigateToTask(page, taskId, projectId);
        await expect(page.locator('.task-detail-page')).toBeVisible();
        // Switch to the task explorer view (same URL param as the in-app button).
        await page.goto(`/?project=${projectId}&task=${taskId}&view=task-explorer`);
        await page.waitForSelector('.terminal-btn[aria-label="Open terminal"]', { timeout: 10000 });

        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible();
        await pressOpenTerminal(page);
        await expectTerminalVisible(page);
        await pressOpenTerminal(page);
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });
    });

    test('does not fire while a modal is open', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        await page.locator(SETTINGS_BTN).first().click();
        await expect(page.locator(PREFERENCES_TAB)).toBeVisible();

        await pressOpenTerminal(page);
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible();
    });
});
