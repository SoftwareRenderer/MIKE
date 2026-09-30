import { test, expect } from '@playwright/test';
import { editTaskTitle, gotoProject, isolatedProject, queuedActionCount } from './helpers';

/**
 * An edit the server refuses goes back on the board with a sentence saying so: the card moves before the request
 * goes out, so a refusal that only reaches the console leaves a card claiming a title nobody saved. A refusal is
 * also not retried — a write the server said no to must not sit in the persisted queue.
 */
isolatedProject();

const REFUSE = {
  status: 403,
  contentType: 'application/json',
  body: JSON.stringify({ error: 'not today' }),
};

const PUT_TASK = '**/api/projects/*/tasks/*';

test.describe('A task edit the server refuses', () => {
  test.beforeEach(async ({ page }) => {
    await gotoProject(page);
    await expect(page.locator('section[aria-label="Kanban board"] .card').first()).toBeVisible({ timeout: 15000 });
  });

  test('the card returns to the title the server has and the failure is announced', async ({ page }) => {
    const card = page.locator('section[aria-label="Kanban board"] .card').first();
    const taskId = (await card.getAttribute('data-task-id')) as string;
    const title = card.locator('.card-title-text');
    const stored = (await title.textContent())?.trim() || '';
    expect(stored.length, 'the card carries a title to put back').toBeGreaterThan(0);

    await page.route(PUT_TASK, route => (route.request().method() === 'PUT' ? route.fulfill(REFUSE) : route.continue()));

    await editTaskTitle(page, taskId, `refused-edit-${Date.now()}`);

    await expect(title).toContainText(stored, { timeout: 10000 });
    await expect(page.locator('.toast.error')).toContainText('not today', { timeout: 10000 });
    expect(await queuedActionCount(page), 'a refusal is an answer, not a retry').toBe(0);

    await page.unroute(PUT_TASK);
  });
});
