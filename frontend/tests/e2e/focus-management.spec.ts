import { test, expect, Page, Locator } from '@playwright/test';
import { getProjectId } from './helpers';

// ============================================================
// FOCUS MANAGEMENT — inputs that appear on user interaction
// ============================================================
// Rows/forms inserted after a click must receive keyboard focus
// programmatically (the browser ignores `autofocus` on inserted
// elements). This spec guards that behavior.

/** Assert that the currently focused element matches `selector`. */
async function expectFocused(page: Page, selector: string) {
    await expect
        .poll(async () =>
            page.evaluate((sel) => {
                const el = document.activeElement;
                return el instanceof Element ? el.matches(sel) : false;
            }, selector),
        )
        .toBe(true);
}

/** Click the toggle, assert the row appears focused, then dismiss it.
 *  Dismiss must be row-specific (cancel button / blur) — these views have
 *  window-level Escape handlers that close whole panels/modals. */
async function expectToggleFocusesInput(
    page: Page,
    scope: Locator,
    toggleSelector: string,
    inputSelector: string,
    dismiss: () => Promise<void>,
) {
    await scope.locator(toggleSelector).click();
    const input = scope.locator(inputSelector);
    await expect(input).toBeVisible({ timeout: 5000 });
    await expectFocused(page, inputSelector);
    await dismiss();
    await expect(input).toBeHidden({ timeout: 5000 });
}

test.describe('Focus management for conditionally rendered inputs', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
    });

    test('login modal focuses the password field', async ({ page }) => {
        const context = page.context();
        await context.clearCookies();
        await page.goto('/');
        await expect(page.locator('#login-password')).toBeVisible({ timeout: 10000 });
        await expectFocused(page, '#login-password');
    });

    test('Add Project modal focuses new-folder input and clone URL', async ({ page }) => {
        await page.getByRole('button', { name: 'Add Project' }).click();
        const dialog = page.locator('[role="dialog"]');
        const newFolderBtn = dialog.locator('button[aria-label="New folder"]');
        await newFolderBtn.waitFor({ timeout: 5000 });

        await expectToggleFocusesInput(
            page,
            dialog,
            'button[aria-label="New folder"]',
            '.folder-name-input',
            () => dialog.locator('button.btn-mini-cancel').click(),
        );

        await dialog.locator('.tab', { hasText: 'Clone from Git' }).click();
        await expect(dialog.locator('#clone-url')).toBeVisible({ timeout: 5000 });
        await expectFocused(page, '#clone-url');
    });

    test('project explorer focuses new-file and new-folder inputs', async ({ page }) => {
        await page.locator('.project-item').first().click();
        await page.waitForSelector('.top-bar', { timeout: 10000 });
        await page.getByRole('button', { name: 'Open explorer' }).click();
        const fb = page.locator('.explorer-view .file-browser');
        await fb.locator('button[aria-label="Create new file"]').waitFor({ timeout: 10000 });

        // Blur to dismiss (onblur closes the row) — Escape would close the whole view
        const blur = () => fb.locator('.file-browser-label').click();

        await expectToggleFocusesInput(
            page,
            fb,
            'button[aria-label="Create new file"]',
            'input[aria-label="New file name"]',
            blur,
        );
        await expectToggleFocusesInput(
            page,
            fb,
            'button[aria-label="Create new folder"]',
            'input[aria-label="New folder name"]',
            blur,
        );
    });

    test('task explorer focuses new-file and new-folder inputs', async ({ page }) => {
        const projectId = await getProjectId(page);
        if (!projectId) return;

        await page.locator('.project-item').first().click();
        await page.waitForSelector('.card', { timeout: 10000 });
        if ((await page.locator('.card').count()) === 0) return;
        await page.locator('.card').first().click();
        await page.waitForSelector('.task-detail-page', { timeout: 10000 });

        // The task detail header has its own explorer button (no .explorer-btn class)
        await page.getByRole('button', { name: 'Open explorer' }).click();
        const tfb = page.locator('.task-file-browser');
        await tfb.locator('button[aria-label="Create new file"]').waitFor({ timeout: 10000 });

        const blur = () => tfb.locator('.file-browser-label').click();

        await expectToggleFocusesInput(
            page,
            tfb,
            'button[aria-label="Create new file"]',
            'input[aria-label="New file name"]',
            blur,
        );
        await expectToggleFocusesInput(
            page,
            tfb,
            'button[aria-label="Create new folder"]',
            'input[aria-label="New folder name"]',
            blur,
        );
    });
});
