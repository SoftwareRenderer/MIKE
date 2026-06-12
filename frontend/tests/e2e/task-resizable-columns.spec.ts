import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Resizable Columns', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show resize handles between columns', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `resize-handles-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Two resize handles should be present (Board↔Chat, Chat↔Diff)
    const handles = page.locator('[role="separator"]');
    await expect(handles).toHaveCount(2);
  });

  test('should resize board column via drag', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `resize-drag-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const handle = page.locator('[role="separator"]').first();
    await expect(handle).toBeVisible();

    // Get initial width of the board column
    const initialWidth = await page.locator('.column-context').boundingBox();
    expect(initialWidth).not.toBeNull();

    // Drag the handle to the right (widen the board column)
    await handle.dragTo(page.locator('.column-chat'), {
      targetPosition: { x: 100, y: 100 },
      force: true,
    });

    // Board column should be wider
    const newWidth = await page.locator('.column-context').boundingBox();
    expect(newWidth).not.toBeNull();
    expect(newWidth!.width).toBeGreaterThan(initialWidth!.width);
  });

  test('should persist column sizes to localStorage', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `resize-persist-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const handle = page.locator('[role="separator"]').first();

    // Resize the board column
    await handle.dragTo(page.locator('.column-chat'), {
      targetPosition: { x: 80, y: 100 },
      force: true,
    });

    // Check that localStorage has the ratio
    const boardRatio = await page.evaluate(() => {
      return localStorage.getItem('kanban:detail:boardRatio');
    });
    expect(boardRatio).not.toBeNull();
    const ratio = Number.parseFloat(boardRatio!);
    expect(ratio).toBeGreaterThan(0.12); // MIN_BOARD_RATIO
    expect(ratio).toBeLessThan(0.40);   // MAX_BOARD_RATIO
  });

  test('should restore column sizes from localStorage', async ({ page }) => {
    // Set a custom ratio in localStorage
    await page.evaluate(() => {
      localStorage.setItem('kanban:detail:boardRatio', '0.30');
    });

    // Create own task and open it
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `resize-restore-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Check that the board column uses the persisted ratio
    const boardWidth = await page.locator('.column-context').evaluate(el => {
      return Number.parseFloat(el.style.width);
    });
    expect(boardWidth).toBeCloseTo(30, 0); // ~30%
  });

  test('should clamp board column within min/max bounds', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `resize-clamp-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    const handle = page.locator('[role="separator"]').first();

    // Try to drag far left (below min)
    await handle.dragTo(page.locator('.btn-back'), {
      targetPosition: { x: 5, y: 5 },
      force: true,
    });

    // Board should not be smaller than min (12%)
    const minWidth = await page.locator('.column-context').boundingBox();
    const columnsBox = await page.locator('.columns').boundingBox();
    expect(minWidth).not.toBeNull();
    expect(columnsBox).not.toBeNull();
    const minPercent = (minWidth!.width / columnsBox!.width) * 100;
    expect(minPercent).toBeGreaterThanOrEqual(11); // Allow small margin for rounding
  });
});
