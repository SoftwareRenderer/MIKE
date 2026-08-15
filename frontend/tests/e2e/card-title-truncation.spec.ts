import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, navigateToTask } from './helpers';

// ============================================================
// CARD TITLE TRUNCATION — subtask title must not overlap the
// action buttons (e.g. the "Create subtask" button) in the
// project view and in the task view's side board.
//
// Scenario: an in-progress subtask in the idle state renders
// three action elements (Create subtask + Edit + Idle badge).
// The title padding must account for all three so the title
// text never overlaps the buttons.
// ============================================================

async function createTask(page: any, projectId: string, title: string, prompt: string, extra: Record<string, unknown> = {}): Promise<any> {
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: { title, prompt, ...extra },
    });
    if (!result.ok) throw new Error(`createTask: API returned ${result.status}: ${JSON.stringify(result.data)}`);
    return result.data;
}

/**
 * Hover the card's title row and assert the title text does not extend
 * past the left edge of the leftmost action element.
 */
async function expectNoTitleOverlap(page: any, card: any) {
    await card.locator('.card-title-text').first().hover();
    await page.waitForTimeout(400); // let the padding-right transition settle
    const metrics = await card.evaluate((cardEl: Element) => {
        const title = cardEl.querySelector('.card-title-text') as HTMLElement;
        const actions = cardEl.querySelector('.card-actions') as HTMLElement;
        if (!title || !actions) return null;
        const tb = title.getBoundingClientRect();
        const cs = getComputedStyle(title);
        const actionEls = [...actions.querySelectorAll('.action-icon, .action-idle')];
        if (actionEls.length === 0) return null;
        const leftmost = Math.min(...actionEls.map(el => el.getBoundingClientRect().x));
        // Right edge of the text area (content box excludes padding-right)
        const textRight = tb.x + tb.width - parseFloat(cs.paddingRight || '0');
        return { textRight, leftmost };
    });
    expect(metrics, 'card should have title text and visible action elements').toBeTruthy();
    // Allow a tiny epsilon for sub-pixel rounding
    expect(metrics!.textRight).toBeLessThanOrEqual(metrics!.leftmost + 0.5);
}

test.describe('Card title truncation (subtask action overlap)', () => {
    test('subtask title does not overlap action buttons in project view and side board', async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });
        const projectId = await getProjectId(page);

        const ts = Date.now();
        const parent = await createTask(page, projectId, `Overlap check parent ${ts}`, 'parent prompt', {});
        // Long title so the text fills the card width and must be truncated.
        // Board-view subtask cards always render a "Create subtask" button
        // (showSubtaskBorder is inherited from the top-level card).
        const subtask = await createTask(page, projectId,
            `Subtask with a very long title that should be truncated before reaching the action buttons on the right ${ts}`,
            'do nothing',
            { parent_task_id: parent.id });

        // Put parent and subtask in the in-progress column in the idle state —
        // the state they naturally reach after a run completes (nested cards
        // inherit the parent's column). This renders the Idle badge (third
        // action slot) next to create + edit.
        for (const t of [parent, subtask]) {
            const put = await csrfFetch(page, `/api/projects/${projectId}/tasks/${t.id}`, {
                method: 'PUT',
                body: { status: 'in_progress', execution_state: 'idle' },
            });
            expect(put.ok).toBeTruthy();
        }

        async function cleanup() {
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${subtask.id}`, { method: 'DELETE' });
            await csrfFetch(page, `/api/projects/${projectId}/tasks/${parent.id}`, { method: 'DELETE' });
        }
        // --- Project view: subtask nested under parent card ---
        await page.goto('/');
        await page.waitForSelector(`.card[data-task-id="${parent.id}"]`, { timeout: 10000 });
        const parentCard = page.locator(`.card[data-task-id="${parent.id}"]`).first();
        const progressBar = parentCard.locator('.subtask-progress-bar');
        if (await progressBar.first().isVisible()) {
            if ((await progressBar.first().getAttribute('aria-expanded')) !== 'true') {
                await progressBar.first().click();
            }
        }
        const subCard = page.locator(`.card[data-task-id="${subtask.id}"].is-subtask`).first();
        await expect(subCard).toBeVisible({ timeout: 10000 });
        // The Idle badge proves the third action slot is rendered;
        // the create button is the one the title must not overlap.
        await expect(subCard.locator('.action-idle').first()).toBeVisible();
        await expect(subCard.locator('.action-add').first()).toBeVisible();
        await expectNoTitleOverlap(page, subCard);

        // --- Task view side board: subtask card in the In Progress section ---
        await navigateToTask(page, parent.id, projectId);
        const sideParent = page.locator(`.task-list .card[data-task-id="${parent.id}"]`).first();
        await expect(sideParent).toBeVisible({ timeout: 10000 });
        const sideBar = sideParent.locator('.subtask-progress-bar');
        if (await sideBar.first().isVisible()) {
            if ((await sideBar.first().getAttribute('aria-expanded')) !== 'true') {
                await sideBar.first().click();
            }
        }
        const sideSub = page.locator(`.task-list .card[data-task-id="${subtask.id}"]`).first();
        await expect(sideSub).toBeVisible({ timeout: 10000 });
        await expect(sideSub.locator('.action-idle').first()).toBeVisible();
        await expect(sideSub.locator('.action-add').first()).toBeVisible();
        await expectNoTitleOverlap(page, sideSub);

        await cleanup();
    });
});
