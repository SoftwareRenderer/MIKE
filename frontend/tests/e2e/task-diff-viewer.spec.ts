import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Structured Diff Viewer', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show diff column in task detail', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `diff-empty-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The diff column should exist
    await expect(page.locator('.column-diff')).toBeVisible({ timeout: 5000 });
  });

  test('should show empty diff state for new tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `diff-column-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Diff content area should be visible (empty state)
    await expect(page.locator('.diff-content')).toBeVisible({ timeout: 5000 });
  });
});
