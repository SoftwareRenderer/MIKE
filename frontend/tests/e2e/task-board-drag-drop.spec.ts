import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Task board interactions in task detail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should click a task card in board overview to switch to it', async ({ page }) => {
    const projectId = await getProjectId(page);
    const timestamp = Date.now();
    const taskId1 = await createTaskViaApi(page, projectId, `switch-a-${timestamp}`);
    const taskId2 = await createTaskViaApi(page, projectId, `switch-b-${timestamp}`);

    // Open task 1
    await navigateToTask(page, taskId1);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify we're on task 1
    await expect(page).toHaveURL(new RegExp(`task=.*${taskId1.replace(/-/g, '\\-')}`));

    // Find task 2's card by data-task-id
    const targetCard = page.locator(`.card[data-task-id="${taskId2}"]`);
    await expect(targetCard).toBeVisible({ timeout: 5000 });

    // Click on task 2's card to switch
    await targetCard.click();

    // URL should now reference task 2
    await expect(page).toHaveURL(new RegExp(`task=.*${taskId2.replace(/-/g, '\\-')}`), { timeout: 5000 });
  });

  test('should have draggable cards and drop zones in board overview', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `draggable-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // All 4 section headers should be visible
    const sections = page.locator('.section-header');
    await expect(sections).toHaveCount(4, { timeout: 5000 });

    // Cards should have draggable attribute
    const cards = page.locator('.card');
    const firstCard = cards.first();
    await expect(firstCard).toBeVisible({ timeout: 5000 });
    await expect(firstCard).toHaveAttribute('draggable', 'true');
  });
});
