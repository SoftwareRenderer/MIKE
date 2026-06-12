import { test, expect, getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Task Management', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should display the board with columns', async ({ page }) => {
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4);
  });

  test('should create a new task', async ({ page }) => {
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', 'Test task from e2e test');
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });
    const cards = page.locator('.card');
    await expect(cards.first()).toBeVisible();
  });

  test('should open task detail view', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `basic-open-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.project-prefix')).toBeVisible();
  });

  test('should navigate back to board from task detail', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `basic-nav-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await page.click('.btn-back');
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });

  test('should show sidebar hidden when task detail is open', async ({ page }) => {
    await expect(page.locator('.sidebar')).toBeVisible();
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `basic-sidebar-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.sidebar')).not.toBeVisible();
  });

  test('should have 3-column layout in task detail', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `basic-columns-${Date.now()}`);
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const columns = page.locator('.columns .column');
    await expect(columns).toHaveCount(3);
    await expect(page.locator('.column-context')).toBeVisible();
    await expect(page.locator('.column-chat')).toBeVisible();
    await expect(page.locator('.column-diff')).toBeVisible();
  });

});
