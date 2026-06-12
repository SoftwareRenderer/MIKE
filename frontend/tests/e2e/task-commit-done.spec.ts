import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Commit and Mark as Done', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show Commit and Mark as Done button for review tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `commit-review-${Date.now()}`);

    // Move task to review status
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    // Open the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The "Commit and Mark as Done" button should be visible above the chat form
    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).toBeVisible({ timeout: 5000 });
    await expect(commitButton).toContainText('Commit and Mark as Done');
  });

  test('should not show Commit and Mark as Done button for non-review tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `commit-nonreview-${Date.now()}`);

    // Task is in backlog by default
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Button should not be visible for backlog tasks
    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).not.toBeVisible();
  });

  test('should commit and move task to done when button is clicked', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `commit-done-${Date.now()}`);

    // Move task to review status
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    // Open the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify button is visible
    const commitButton = page.locator('.btn-commit-done');
    await expect(commitButton).toBeVisible({ timeout: 5000 });

    // Click the button
    await commitButton.click();

    // Wait for the button to disappear (task moved to done)
    await expect(commitButton).not.toBeVisible({ timeout: 10000 });

    // Verify task status is now "done" via API
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    expect(taskData.status).toBe('done');
    expect(taskData.status_state).toBe('completed');
  });

  test('should not show approve/reject buttons in header for review tasks', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `commit-header-${Date.now()}`);

    // Move task to review status
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'review' }),
      });
    }, { pid: projectId, tid: taskId });

    // Open the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // The old approve/reject buttons should NOT be in the header
    const approveButton = page.locator('[aria-label="Approve and move to Done"]');
    const rejectButton = page.locator('[aria-label="Reject and return to In Progress"]');
    await expect(approveButton).not.toBeVisible();
    await expect(rejectButton).not.toBeVisible();
  });
});
