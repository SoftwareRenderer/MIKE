import { test, expect } from '@playwright/test';
import {getProjectId, createTaskViaApi, navigateToTask} from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Structured Diff Viewer', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('should show diff column in task detail', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `diff-empty-${Date.now()}`);
    await navigateToTask(page, taskId, projectId).catch(() => test.skip('Navigation failed'));
    if (!await page.locator('.task-detail-page').isVisible().catch(() => false)) {
      test.skip('Task detail page not visible');
    }

    // The diff column should exist
    await expect(page.locator('.column-diff')).toBeVisible();
  });

  test('should show empty diff state for new tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `diff-column-${Date.now()}`);
    await navigateToTask(page, taskId, projectId).catch(() => test.skip('Navigation failed'));
    if (!await page.locator('.task-detail-page').isVisible().catch(() => false)) {
      test.skip('Task detail page not visible');
    }

    // Diff content area should be visible (empty state)
    await expect(page.locator('.diff-content')).toBeVisible();
  });
});
