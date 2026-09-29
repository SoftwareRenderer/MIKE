import { test, expect, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, getProjectId, createTaskViaApi } from './helpers';

// Model badge visibility preference (Settings → Preferences → UI). The model
// badge is shown on task cards by default; "Hide model badge" removes it.
// The preference persists server-side (hide_model_badge in /api/config).
//
// Runs on a lane server (`ISOLATED_SPECS`): mutates server config via the UI Save flow.

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const PREFERENCES_TAB = 'button.tab-btn:has-text("Preferences")';
const HIDE_MODEL_BADGE_TOGGLE = 'button[aria-label="Hide model badge"]';
const SAVE_BTN = 'button.btn-primary[title="Save settings"]';
const MODEL_BADGE = '.card .model-badge';

/**
 * Self-consistent, saveable provider config (the Settings Save flow validates
 * that worker/utility references resolve). A worker model is set so task
 * cards render a model badge.
 */
async function ensureSaveableConfig(page: Page) {
    const cfg = await apiGetConfig(page);
    const maxId = Math.max(0, ...((cfg?.providers || []) as Array<{ id?: number }>).map(p => p.id || 0));
    const id = maxId + 1;
    await apiPutConfig(page, {
        providers: [{
            id,
            name: 'model-badge-mock',
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

/** Navigate to the board and make sure at least one card shows a model badge. */
async function ensureCardWithModelBadge(page: Page) {
    await page.goto('/');
    await page.waitForSelector('.add-task-btn', { timeout: 10000 });
    if (await page.locator(MODEL_BADGE).first().isVisible().catch(() => false)) return;
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, `model-badge-${Date.now()}`);
    await page.reload();
    await page.waitForSelector(MODEL_BADGE, { timeout: 10000 });
}

test.describe.serial('Hide model badge preference', () => {
    test.beforeEach(async ({ page }) => {
        await ensureSaveableConfig(page);
    });

    test('model badge is shown on task cards by default', async ({ page }) => {
        await ensureCardWithModelBadge(page);
        await expect(page.locator(MODEL_BADGE).first()).toBeVisible();
    });

    test('toggling hides the model badge on task cards and persists', async ({ page }) => {
        await ensureCardWithModelBadge(page);

        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(HIDE_MODEL_BADGE_TOGGLE)).toBeVisible();
        await expect(page.locator(HIDE_MODEL_BADGE_TOGGLE)).toHaveAttribute('aria-checked', 'false');

        await page.locator(HIDE_MODEL_BADGE_TOGGLE).click();
        await expect(page.locator(HIDE_MODEL_BADGE_TOGGLE)).toHaveAttribute('aria-checked', 'true');

        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        await expect(page.locator(MODEL_BADGE)).toHaveCount(0);
        await waitForConfig(page, c => c.hide_model_badge === true);
    });

    test('toggling off restores the model badge', async ({ page }) => {
        // Carries over from the previous test: preference is on (badge hidden).
        const projectId = await getProjectId(page);
        await createTaskViaApi(page, projectId, `model-badge-restore-${Date.now()}`);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        await expect(page.locator(MODEL_BADGE)).toHaveCount(0);

        await page.locator(SETTINGS_BTN).first().click();
        await page.locator(PREFERENCES_TAB).click();
        await expect(page.locator(HIDE_MODEL_BADGE_TOGGLE)).toHaveAttribute('aria-checked', 'true');

        await page.locator(HIDE_MODEL_BADGE_TOGGLE).click();
        await page.locator(SAVE_BTN).click();
        await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });

        await expect(page.locator(MODEL_BADGE).first()).toBeVisible({ timeout: 5000 });
        await waitForConfig(page, c => c.hide_model_badge === false);
    });

    test('preference persists server-side across reloads', async ({ page }) => {
        // Carries over from the previous test: preference is off.
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        await apiPutConfig(page, { hide_model_badge: true });
        await waitForConfig(page, c => c.hide_model_badge === true);
        await page.reload();
        await page.waitForSelector('.card', { timeout: 10000 });
        await expect(page.locator(MODEL_BADGE)).toHaveCount(0);

        // Restore for other suites
        await apiPutConfig(page, { hide_model_badge: false });
        await waitForConfig(page, c => c.hide_model_badge === false);
    });
});
