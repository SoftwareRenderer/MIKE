import { test, expect, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, getProjectId, createTaskViaApi, csrfFetch } from './helpers';

// Board sort direction (Settings → Preferences → UI). By default the card moved most
// recently sits on top of its column; "Oldest moved on top" flips that. The preference
// persists server-side (sort_oldest_first in /api/config).
//
// Runs in the `isolated` project: mutates server config via the UI Save flow.

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const PREFERENCES_TAB = 'button.tab-btn:has-text("Preferences")';
const SORT_TOGGLE = 'button[aria-label="Oldest moved on top"]';
const SAVE_BTN = 'button.btn-primary[title="Save settings"]';
const REVIEW_COLUMN = 2;

/** Self-consistent, saveable provider config (the Settings Save flow validates that
 *  worker/utility references resolve). */
async function ensureSaveableConfig(page: Page) {
    const cfg = await apiGetConfig(page);
    const maxId = Math.max(0, ...((cfg?.providers || []) as Array<{ id?: number }>).map(p => p.id || 0));
    const id = maxId + 1;
    await apiPutConfig(page, {
        providers: [{
            id,
            name: 'sort-order-mock',
            url: 'http://localhost:9999/v1',
            models: [{ name: 'mock-model', reasoning_effort: 'off' }],
            concurrency: 1,
        }],
        worker_provider: String(id),
        worker_model: 'mock-model',
        utility_provider: String(id),
        utility_model: 'mock-model',
        sort_oldest_first: false,
    });
    await waitForConfig(page, c => c.worker_provider === String(id) && c.utility_provider === String(id));
}

/** Three Review-column cards moved in a known order: ids[0] moved first, ids[2] last. */
async function createMovedTasks(page: Page): Promise<string[]> {
    const projectId = await getProjectId(page);
    const ids: string[] = [];
    for (let i = 0; i < 3; i++) {
        ids.push(await createTaskViaApi(page, projectId, `sort-order-${i}-${Date.now()}`));
        if (i > 0) await page.waitForTimeout(1100); // moved_at has second precision
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${ids[i]}/move`, {
            method: 'POST',
            body: { status: 'review' },
        });
    }
    return ids;
}

function reviewOrder(page: Page, ids: string[]) {
    return page.locator('.column').nth(REVIEW_COLUMN).locator('.card').evaluateAll(
        (cards: Element[]) => cards.map(c => c.getAttribute('data-task-id')).filter(Boolean) as string[]
    ).then((order: string[]) => order.filter(id => ids.includes(id)));
}

async function expectOrder(page: Page, ids: string[], expected: string[]) {
    await expect(async () => {
        expect(await reviewOrder(page, ids)).toEqual(expected);
    }).toPass({ timeout: 10000 });
}

/** Flip the preference in the UI and save. */
async function setPreference(page: Page, want: boolean) {
    await page.locator(SETTINGS_BTN).first().click();
    await page.locator(PREFERENCES_TAB).click();
    await expect(page.locator(SORT_TOGGLE)).toBeVisible();
    await expect(page.locator(SORT_TOGGLE)).toHaveAttribute('aria-checked', String(!want));
    await page.locator(SORT_TOGGLE).click();
    await expect(page.locator(SORT_TOGGLE)).toHaveAttribute('aria-checked', String(want));
    await page.locator(SAVE_BTN).click();
    await expect(page.locator('.config-modal')).not.toBeVisible({ timeout: 5000 });
    await waitForConfig(page, c => c.sort_oldest_first === want);
}

test.describe.serial('Board sort direction preference', () => {
    test.beforeEach(async ({ page }) => {
        await ensureSaveableConfig(page);
    });

    test('keeps the most recently moved card on top by default', async ({ page }) => {
        const ids = await createMovedTasks(page);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        // Unset (or false) is the newest-move-first default.
        const cfg = await apiGetConfig(page);
        expect(cfg?.sort_oldest_first ?? false).toBe(false);
        await expectOrder(page, ids, [ids[2], ids[1], ids[0]]);
    });

    test('toggling puts the last-moved card at the bottom and persists', async ({ page }) => {
        const ids = await createMovedTasks(page);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        await expectOrder(page, ids, [ids[2], ids[1], ids[0]]);

        await setPreference(page, true);

        // Re-orders without a reload...
        await expectOrder(page, ids, [ids[0], ids[1], ids[2]]);
        // ...and survives one.
        await page.reload();
        await page.waitForSelector('.card', { timeout: 10000 });
        await expectOrder(page, ids, [ids[0], ids[1], ids[2]]);

        // The direction follows the move, not creation order. Moving ids[0] into a
        // different column and back makes it the newest move, which now sorts last.
        const projectId = await getProjectId(page);
        await page.waitForTimeout(1100);
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${ids[0]}/move`, {
            method: 'POST',
            body: { status: 'in_progress' },
        });
        await page.waitForTimeout(1100);
        await csrfFetch(page, `/api/projects/${projectId}/tasks/${ids[0]}/move`, {
            method: 'POST',
            body: { status: 'review' },
        });
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        await expectOrder(page, ids, [ids[1], ids[2], ids[0]]);
    });

    test('toggling back restores newest-move-first', async ({ page }) => {
        // beforeEach seeds a saveable config, so start from the preference being on.
        await apiPutConfig(page, { sort_oldest_first: true });
        await waitForConfig(page, c => c.sort_oldest_first === true);

        const ids = await createMovedTasks(page);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
        await expectOrder(page, ids, [ids[0], ids[1], ids[2]]);

        await setPreference(page, false);
        await expectOrder(page, ids, [ids[2], ids[1], ids[0]]);

        // Restore for other suites
        await apiPutConfig(page, { sort_oldest_first: false });
        await waitForConfig(page, c => c.sort_oldest_first === false);
    });
});
