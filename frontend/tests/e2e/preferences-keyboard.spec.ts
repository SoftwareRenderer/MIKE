import { test, expect, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, getProjectId, createTaskViaApi } from './helpers';

// Keyboard shortcut preferences (Settings → Preferences). The create-task
// shortcut defaults to "C"; can be rebound, reset, or cleared. Bindings
// persist server-side (keyboard_shortcuts in /api/config).
//
// Runs on a lane server (`ISOLATED_SPECS`): mutates server config via the UI Save flow.

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const PREFERENCES_TAB = 'button.tab-btn:has-text("Preferences")';
// Rows in the Preferences tab (two shortcuts share class/aria names).
const CREATE_ROW = '.shortcut-row[data-shortcut="create_task"]';
const SUBMIT_ROW = '.shortcut-row[data-shortcut="create_task_submit"]';
const TERMINAL_ROW = '.shortcut-row[data-shortcut="open_terminal"]';
const FULLSCREEN_ROW = '.shortcut-row[data-shortcut="toggle_fullscreen"]';
const KEY_CHIP = `${CREATE_ROW} button.key-chip`;
const SUBMIT_CHIP = `${SUBMIT_ROW} button.key-chip`;
const TERMINAL_CHIP = `${TERMINAL_ROW} button.key-chip`;
const FULLSCREEN_CHIP = `${FULLSCREEN_ROW} button.key-chip`;
const TERMINAL_RESET = `${TERMINAL_ROW} button[aria-label="Reset to default"]`;
const TERMINAL_CLEAR = `${TERMINAL_ROW} button[aria-label="Clear shortcut"]`;
const FULLSCREEN_RESET = `${FULLSCREEN_ROW} button[aria-label="Reset to default"]`;
const MULTI_ROW = '.shortcut-row[data-shortcut="multi_select_click"]';
const MULTI_CHIP = `${MULTI_ROW} button.key-chip`;
const RESET_BTN = `${CREATE_ROW} button[aria-label="Reset to default"]`;
const CLEAR_BTN = `${CREATE_ROW} button[aria-label="Clear shortcut"]`;
const SAVE_BTN = 'button.btn-primary[title="Save settings"]';
const BOARD_BTN = '.add-task-btn';
const CREATE_MODAL = '#task-description';
const TERMINAL_PANEL = '.terminal-panel';
const TERMINAL_CLOSE = '.terminal-panel .close-btn';

/**
 * Self-consistent, saveable provider config (the Settings Save flow validates
 * that worker/utility references resolve). Does not touch keyboard_shortcuts.
 */
async function ensureSaveableConfig(page: Page) {
    const cfg = await apiGetConfig(page);
    const maxId = Math.max(0, ...((cfg?.providers || []) as Array<{ id?: number }>).map(p => p.id || 0));
    const id = maxId + 1;
    await apiPutConfig(page, {
        providers: [{
            id,
            name: 'keyboard-pref-mock',
            url: 'http://localhost:9999/v1',
            models: [{ name: 'mock-model', reasoning_effort: 'off' }],
            concurrency: 1,
        }],
        worker_provider: String(id),
        worker_model: 'mock-model',
        utility_provider: String(id),
        utility_model: 'mock-model',
    });
    await waitForConfig(page, c => c.worker_provider === String(id) && c.utility_provider === String(id));
}

/** Reset the create-task binding to its default via the API. */
async function resetShortcuts(page: Page) {
    await apiPutConfig(page, { keyboard_shortcuts: {} });
}

test.describe.serial('Keyboard shortcut preferences', () => {
    test.beforeEach(async ({ page }) => {
        await ensureSaveableConfig(page);
        await page.goto('/');
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });
    });

    test('default: button shows a C keycap and pressing C opens the create-task modal', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        await expect(page.locator(BOARD_BTN)).toContainText('+ Create Task');
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('C');
        await expect(page.locator(BOARD_BTN)).toHaveAttribute('title', 'Create a new task (C)');

        await page.keyboard.press('c');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });

        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });
    });

    test('rebinding to Shift+K via the Preferences tab', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        // Open Settings → Preferences and capture a new key
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('C');

        await page.locator(KEY_CHIP).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('Press a key...');
        await page.keyboard.press('Shift+K');
        await expect(page.locator(KEY_CHIP)).toHaveText('Shift+K');

        // Save and verify the button hint updates
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('Shift+K');
        await expect(page.locator(BOARD_BTN)).toHaveAttribute('title', 'Create a new task (Shift+K)');

        // The old key no longer triggers; the new one does
        await page.keyboard.press('c');
        await page.waitForTimeout(300);
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible();

        await page.keyboard.press('Shift+K');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });
    });

    test('reset restores the default binding', async ({ page }) => {
        // Carries over from the rebind test: binding is Shift+K
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('Shift+K');

        await page.locator(RESET_BTN).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('C');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('C');

        await page.keyboard.press('c');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });
    });

    test('clearing disables the shortcut and hides the key hint', async ({ page }) => {
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('C');

        await page.locator(CLEAR_BTN).click();
        await expect(page.locator(KEY_CHIP)).toHaveText('None');
        // Reset is available to restore the default from the disabled state
        await expect(page.locator(RESET_BTN)).toBeVisible();

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // No key hint or tooltip shortcut; pressing the key does nothing
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).not.toBeAttached();
        await expect(page.locator(BOARD_BTN)).toHaveAttribute('title', 'Create a new task');
        await page.keyboard.press('c');
        await page.waitForTimeout(300);
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible();

        // Restore the default for other suites
        await resetShortcuts(page);
    });

    test('bindings persist server-side in /api/config', async ({ page }) => {
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator(KEY_CHIP).click();
        await page.keyboard.press('Control+K');
        await expect(page.locator(KEY_CHIP)).toHaveText('Ctrl+K');
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // Server-side persistence check
        await waitForConfig(page, c =>
            c.keyboard_shortcuts && c.keyboard_shortcuts.create_task === 'control+k'
        );
        const config = await apiGetConfig(page);
        expect(config.keyboard_shortcuts.create_task).toBe('control+k');

        // The bound combo triggers the modal
        await page.keyboard.press('Control+K');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });

        // Restore the default for other suites
        await resetShortcuts(page);
    });

    test('rebinding submit to Shift+Enter makes it the only trigger', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        // Linux test env: the submit row shows the Ctrl+Enter default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(SUBMIT_CHIP)).toHaveText('Ctrl+Enter');

        await page.locator(SUBMIT_CHIP).click();
        await expect(page.locator(SUBMIT_CHIP)).toHaveText('Press a key...');
        await page.keyboard.press('Shift+Enter');
        await expect(page.locator(SUBMIT_CHIP)).toHaveText('Shift+Enter');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.create_task_submit === 'shift+enter');

        await page.locator(BOARD_BTN).first().click();
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        const prompt = `submit-rebind-${Date.now()}`;
        await page.fill(CREATE_MODAL, prompt);

        await page.keyboard.press('Control+Enter');
        await expect(page.locator(CREATE_MODAL)).toBeVisible();

        await page.keyboard.press('Shift+Enter');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });
        await expect(page.locator('.card', { hasText: prompt }).first()).toBeVisible({ timeout: 10000 });

        // Restore the default for other suites
        await resetShortcuts(page);
    });

    test('hiding hints removes keycaps but keeps the tooltips', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });
        // Board button: keycap visible, tooltip shows the shortcut
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('C');
        await expect(page.locator(BOARD_BTN)).toHaveAttribute('title', 'Create a new task (C)');

        // Enable "Hide shortcut hints"
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator('button[aria-label="Hide shortcut hints"]').click();
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // Board: keycap gone, tooltip keeps the shortcut
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).not.toBeAttached();
        await expect(page.locator(BOARD_BTN)).toHaveAttribute('title', 'Create a new task (C)');

        // Dialog: keycap gone, tooltip keeps the shortcut
        await page.keyboard.press('c');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        const createBtn = page.locator('.btn-primary', { hasText: 'Create Task' });
        await expect(createBtn.locator('kbd.key-hint')).not.toBeAttached();
        await expect(createBtn).toHaveAttribute('title', 'Create the task (Ctrl+Enter)');
        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });

        // Toggle off -> keycaps return
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator('button[aria-label="Hide shortcut hints"]').click();
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('C');

        // Restore defaults for other suites (clears bindings + hide_hints)
        await resetShortcuts(page);
    });

    test('shortcut is suppressed while a modal is open', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });
        await expect(page.locator(BOARD_BTN).locator('.key-hint')).toHaveText('C');

        // With Settings open, "c" must not open the create-task modal
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.keyboard.press('c');
        await page.waitForTimeout(300);
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible();

        // Close settings via Cancel (no changes)
        await page.locator('button[title="Cancel and discard changes"]').click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // Back on the board, "c" works again
        await page.keyboard.press('c');
        await expect(page.locator(CREATE_MODAL)).toBeVisible({ timeout: 5000 });
        await page.keyboard.press('Escape');
        await expect(page.locator(CREATE_MODAL)).not.toBeVisible({ timeout: 5000 });
    });

    test('open_terminal defaults to Ctrl+` and opens the terminal panel', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        // Terminal button tooltip shows the shortcut
        await expect(page.locator('[aria-label="Open terminal"]')).toHaveAttribute('title', 'Open terminal (Ctrl+`)');

        // Preferences row shows the default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('Ctrl+`');

        // Close settings via Cancel (no changes)
        await page.locator('button[title="Cancel and discard changes"]').click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // Pressing the default opens the terminal panel
        await page.keyboard.press('Control+`');
        await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 10000 });
        await page.locator('.terminal-panel .close-btn').click();
        await expect(page.locator('.terminal-panel')).not.toBeVisible({ timeout: 5000 });
    });

    test('rebinding open_terminal persists and the new combo opens the terminal', async ({ page }) => {
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('Ctrl+`');

        await page.locator(TERMINAL_CHIP).click();
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('Press a key...');
        await page.keyboard.press('Alt+Shift+T');
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('Shift+Alt+T');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.open_terminal === 'shift+alt+t');

        await page.keyboard.press('Control+`');
        await page.waitForTimeout(300);
        await expect(page.locator('.terminal-panel')).not.toBeVisible();

        await page.keyboard.press('Alt+Shift+T');
        await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 10000 });
        await page.locator('.terminal-panel .close-btn').click();
        await expect(page.locator('.terminal-panel')).not.toBeVisible({ timeout: 5000 });
    });

    test('reset and clear work for open_terminal', async ({ page }) => {
        // Carries over from the rebind test: binding is Shift+Alt+T
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();

        await page.locator(TERMINAL_RESET).click();
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('Ctrl+`');
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.open_terminal === undefined);

        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator(TERMINAL_CLEAR).click();
        await expect(page.locator(TERMINAL_CHIP)).toHaveText('None');
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.open_terminal === '');

        // Disabled: the default combo does nothing
        await page.keyboard.press('Control+`');
        await page.waitForTimeout(300);
        await expect(page.locator('.terminal-panel')).not.toBeVisible();

        // Restore the default for other suites
        await resetShortcuts(page);
    });

    test('toggle_fullscreen defaults to Ctrl+Up and the default binding toggles the focused area', async ({ page }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        // The Preferences row shows the default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(FULLSCREEN_CHIP)).toHaveText('Ctrl+Up');

        // Close settings via Cancel (no changes)
        await page.locator('button[title="Cancel and discard changes"]').click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        // The default combo toggles the terminal fullscreen (panel has focus)
        await page.keyboard.press('Control+`');
        await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 10000 });
        await page.keyboard.press('Control+ArrowUp');
        await expect(page.locator(TERMINAL_PANEL)).toHaveClass(/fullscreen/);
        await page.keyboard.press('Control+ArrowUp');
        await expect(page.locator(TERMINAL_PANEL)).not.toHaveClass(/fullscreen/);
        await page.locator(TERMINAL_CLOSE).click();
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });
    });

    test('rebinding toggle_fullscreen changes the trigger and reset restores the default', async ({ page }) => {
        // Carries over from the previous test: binding is the default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(FULLSCREEN_CHIP)).toHaveText('Ctrl+Up');

        await page.locator(FULLSCREEN_CHIP).click();
        await expect(page.locator(FULLSCREEN_CHIP)).toHaveText('Press a key...');
        await page.keyboard.press('Shift+Alt+F');
        await expect(page.locator(FULLSCREEN_CHIP)).toHaveText('Shift+Alt+F');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.toggle_fullscreen === 'shift+alt+f');

        await page.keyboard.press('Control+`');
        await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 10000 });
        await page.keyboard.press('Control+ArrowUp');
        await page.waitForTimeout(300);
        await expect(page.locator(TERMINAL_PANEL)).not.toHaveClass(/fullscreen/);

        await page.keyboard.press('Shift+Alt+F');
        await expect(page.locator(TERMINAL_PANEL)).toHaveClass(/fullscreen/);

        // Exit fullscreen first — the fullscreen panel covers the top bar.
        await page.keyboard.press('Shift+Alt+F');
        await expect(page.locator(TERMINAL_PANEL)).not.toHaveClass(/fullscreen/);

        // Close the terminal — its backdrop overlay blocks the Settings button.
        await page.locator(TERMINAL_CLOSE).click();
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });

        // Reset restores the default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator(FULLSCREEN_RESET).click();
        await expect(page.locator(FULLSCREEN_CHIP)).toHaveText('Ctrl+Up');
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.toggle_fullscreen === undefined);

        // Reopen the terminal: Ctrl+Up works again
        await page.keyboard.press('Control+`');
        await expect(page.locator(TERMINAL_PANEL)).toBeVisible({ timeout: 10000 });
        await page.keyboard.press('Control+ArrowUp');
        await expect(page.locator(TERMINAL_PANEL)).toHaveClass(/fullscreen/);

        await page.keyboard.press('Control+ArrowUp');
        await page.locator(TERMINAL_CLOSE).click();
        await expect(page.locator(TERMINAL_PANEL)).not.toBeVisible({ timeout: 5000 });

        // Restore the default for other suites
        await resetShortcuts(page);
    });

    test('rebinding the multi-select click frees Ctrl+Click for the browser', async ({ page, context }) => {
        await resetShortcuts(page);
        await page.reload();
        await page.waitForSelector(BOARD_BTN, { timeout: 10000 });

        // A card to click on
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `multi-rebind-${Date.now()}`);
        const card = page.locator(`.card[data-task-id="${taskId}"]`);
        await expect(card).toBeVisible({ timeout: 5000 });

        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        // Platform default (Chromium here is Linux): Ctrl+Click.
        await expect(page.locator(MULTI_CHIP)).toHaveText(/Click$/);

        // Capturing needs a modifier+click; Escape cancels, a bare click is refused.
        await page.locator(MULTI_CHIP).click();
        await expect(page.locator(MULTI_CHIP)).toHaveText('Now modifier+click...');
        await page.keyboard.press('Escape');
        await expect(page.locator(MULTI_CHIP)).not.toHaveText('Now modifier+click...');

        await page.locator(MULTI_CHIP).click();
        await page.locator('.config-modal .settings-section-title', { hasText: 'Keyboard Shortcuts' }).click({ modifiers: ['Shift'] });
        await expect(page.locator(MULTI_CHIP)).toHaveText('Shift+Click');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.multi_select_click === 'shift+click');

        // Shift+Click now selects cards…
        await card.locator('.card-title-text').click({ modifiers: ['Shift'] });
        await expect(card).toHaveClass(/multi-selected/, { timeout: 5000 });
        await page.keyboard.press('Escape');

        // …and Ctrl+Click is the browser's again: it opens the card in a new tab.
        const popupPromise = context.waitForEvent('page', { timeout: 10000 });
        await card.locator('.card-title-text').click({ modifiers: ['Control'] });
        const tab = await popupPromise;
        expect(tab.url()).toContain(`task=${taskId}`);
        await tab.close();

        // Reset returns the binding (and Ctrl+Click) to the default
        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await page.locator(`${MULTI_ROW} button[aria-label="Reset to default"]`).click();
        await expect(page.locator(MULTI_CHIP)).toHaveText(/Click$/);
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.keyboard_shortcuts?.multi_select_click === undefined);

        // Restore the default for other suites
        await resetShortcuts(page);
    });
});
