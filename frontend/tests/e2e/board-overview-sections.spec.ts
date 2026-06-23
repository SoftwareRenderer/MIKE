import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Board overview sections in task detail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show all 4 sections in the side panel', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `overview-sections-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // All 4 section headers should be visible
    const sections = page.locator('.section-header');
    await expect(sections).toHaveCount(4, { timeout: 5000 });

    // Verify section titles in order
    const sectionTitles = page.locator('.section-title');
    await expect(sectionTitles.nth(0)).toContainText('Backlog');
    await expect(sectionTitles.nth(1)).toContainText('In Progress');
    await expect(sectionTitles.nth(2)).toContainText('Review');
    await expect(sectionTitles.nth(3)).toContainText('Done');
  });

  test('should show sections even when empty', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Create a task in backlog (default status)
    const taskId = await createTaskViaApi(page, projectId, `empty-sections-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // All 4 section groups should be visible (even In Progress, Review, Done are empty)
    const sectionGroups = page.locator('.section-group');
    await expect(sectionGroups).toHaveCount(4, { timeout: 5000 });

    // All 4 section headers should be visible
    const sectionHeaders = page.locator('.section-header');
    await expect(sectionHeaders).toHaveCount(4, { timeout: 5000 });
  });

  test('should have collapsible sections with chevron', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `collapse-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Chevron icons should be present
    const chevrons = page.locator('.section-chevron');
    await expect(chevrons).toHaveCount(4);

    // Section headers should be buttons with aria-expanded
    const sectionHeaders = page.locator('.section-header');
    await expect(sectionHeaders.nth(0)).toHaveAttribute('aria-expanded', 'true');

    // Click to collapse the first section
    await sectionHeaders.nth(0).click();
    await expect(sectionHeaders.nth(0)).toHaveAttribute('aria-expanded', 'false');

    // Click again to expand
    await sectionHeaders.nth(0).click();
    await expect(sectionHeaders.nth(0)).toHaveAttribute('aria-expanded', 'true');
  });
});
