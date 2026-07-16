import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });

test.describe('Toast Notifications', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    // Click on the project to open the board
    await page.click('.project-item');
    await page.waitForSelector('.board', { timeout: 10000 });
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should display error toast when action fails', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-err-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-err-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Intercept start task API calls and return 500 errors
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId1}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId2}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );

    // Multi-select both cards via Ctrl+Click
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });

    // Wait for the multi-select bar to appear
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });

    // Click the Start button
    await page.locator('.bar-btn-start').click();

    // Wait for the confirmation dialog (uses .confirm-message from ConfirmationDialog)
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the error toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.toast')).toHaveAttribute('role', 'alert');
  });

  test('should display success toast when tasks are started', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-suc-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-suc-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Multi-select both cards
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });

    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();

    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the success toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.toast')).toHaveAttribute('role', 'alert');
    const toastText = await page.locator('.toast').textContent();
    expect(toastText).toContain('task');
    expect(toastText).toContain('started');
  });

  test('should auto-dismiss toast after 4 seconds', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-auto-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-auto-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Multi-select both cards
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });

    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();

    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the success toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });

    // Wait for the toast to auto-dismiss (4 seconds + buffer)
    await expect(page.locator('.toast')).not.toBeVisible({ timeout: 6000 });
  });

  test('should dismiss toast when a new toast replaces it', async ({ page }) => {
    const projectId = await getProjectId(page);
    const t1 = await createTaskViaApi(page, projectId, `toast-rpl-1-${Date.now()}`);
    const t2 = await createTaskViaApi(page, projectId, `toast-rpl-2-${Date.now()}`);
    const t3 = await createTaskViaApi(page, projectId, `toast-rpl-3-${Date.now()}`);
    const t4 = await createTaskViaApi(page, projectId, `toast-rpl-4-${Date.now()}`);

    for (const id of [t1, t2, t3, t4]) {
      await expect(page.locator(`[data-task-id="${id}"]`)).toBeVisible({ timeout: 5000 });
    }

    // First start: multi-select t1 and t2
    await page.locator(`[data-task-id="${t1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${t2}"]`).click({ modifiers: ['Control'] });
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // Wait for the first success toast to appear
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    const firstToastText = await page.locator('.toast').textContent();

    // Second start: multi-select t3 and t4 (triggers a second toast that replaces the first)
    await page.locator(`[data-task-id="${t3}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${t4}"]`).click({ modifiers: ['Control'] });
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    // The toast should still be visible (replaced by the second toast)
    await expect(page.locator('.toast')).toBeVisible({ timeout: 10000 });
    const secondToastText = await page.locator('.toast').textContent();

    expect(firstToastText).toContain('task');
    expect(firstToastText).toContain('started');
    expect(secondToastText).toContain('task');
    expect(secondToastText).toContain('started');
  });

  test('should apply error styling for error toasts', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-errsty-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-errsty-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Intercept start task API calls and return 500 errors
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId1}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );
    await page.route(
      `**/api/projects/${projectId}/tasks/${taskId2}/start`,
      (route) => {
        route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'Internal server error' }),
        });
      }
    );

    // Multi-select both cards
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    const toast = page.locator('.toast');
    await expect(toast).toBeVisible({ timeout: 10000 });

    // Verify the error class is applied
    await expect(toast).toHaveClass(/error/);

    // Verify the background color is red (error)
    const bgColor = await toast.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(bgColor).toContain('212'); // #D4332F in rgb contains 212 (red component)
  });

  test('should apply success styling for success toasts', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId1 = await createTaskViaApi(page, projectId, `toast-sucsty-a-${Date.now()}`);
    const taskId2 = await createTaskViaApi(page, projectId, `toast-sucsty-b-${Date.now()}`);

    await expect(page.locator(`[data-task-id="${taskId1}"]`)).toBeVisible({ timeout: 5000 });
    await expect(page.locator(`[data-task-id="${taskId2}"]`)).toBeVisible({ timeout: 5000 });

    // Multi-select both cards
    await page.locator(`[data-task-id="${taskId1}"]`).click({ modifiers: ['Control'] });
    await page.locator(`[data-task-id="${taskId2}"]`).click({ modifiers: ['Control'] });
    await expect(page.locator('.multi-select-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.bar-btn-start')).toBeVisible({ timeout: 5000 });
    await page.locator('.bar-btn-start').click();
    await expect(page.locator('.confirm-message')).toBeVisible({ timeout: 5000 });
    await page.locator('.btn-confirm').click();

    const toast = page.locator('.toast');
    await expect(toast).toBeVisible({ timeout: 10000 });

    // Verify the success class is applied
    await expect(toast).toHaveClass(/success/);

    // Verify the background color is green (success)
    const bgColor = await toast.evaluate((el) => getComputedStyle(el).backgroundColor);
    expect(bgColor).toContain('63'); // #3FB950 in rgb contains 63 (green component)
  });
});
