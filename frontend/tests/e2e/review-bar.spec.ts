import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch, navigateToTask } from './helpers';

test.describe.serial('Review Bar', () => {
  let projectId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
  });

  test('should show review bar when task is in review state', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-bar-appears-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Move task to review via API
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });
    expect(moveResult.ok).toBe(true);

    // Navigate to the task detail page
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar to appear
    const reviewBar = page.locator('.chat-review-bar');
    await expect(reviewBar).toBeVisible({ timeout: 10000 });

    // Should contain the "→ Done" move button
    const doneBtn = reviewBar.locator('.btn-move:has-text("Done")');
    await expect(doneBtn).toBeVisible();

    // Should contain the "→ Backlog" move button
    const backlogBtn = reviewBar.locator('.btn-move:has-text("Backlog")');
    await expect(backlogBtn).toBeVisible();

    // Should contain the action button (Resume or Move to Done depending on entries)
    const actionBtn = reviewBar.locator('.btn-commit-done');
    await expect(actionBtn).toBeVisible();
  });

  test('should move task to Done via → Done button in review bar', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-move-to-done-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Click "→ Done" button (in the move-buttons section)
    const doneBtn = page.locator('.btn-move:has-text("Done")');
    await expect(doneBtn).toBeVisible();
    await doneBtn.click();

    // Verify task moved to done via API
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('done');
    }).toPass({ timeout: 10000 });

    // The review bar should now show done-state buttons (→ Review, → Backlog)
    const reviewBar = page.locator('.chat-review-bar');
    await expect(reviewBar).toBeVisible();
    const toReviewBtn = reviewBar.locator('.btn-move:has-text("Review")');
    await expect(toReviewBtn).toBeVisible();
  });

  test('should move task to Backlog via → Backlog button in review bar', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-move-to-backlog-${Date.now()}`);
    expect(taskId).not.toBe('');

    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Navigate to the task
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for the review bar
    await expect(page.locator('.chat-review-bar')).toBeVisible({ timeout: 10000 });

    // Click "→ Backlog" button
    const backlogBtn = page.locator('.btn-move:has-text("Backlog")');
    await expect(backlogBtn).toBeVisible();
    await backlogBtn.click();

    // Verify task moved to backlog via API
    await expect(async () => {
      const task = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.ok ? res.json() : null;
      }, { pid: projectId, tid: taskId });
      expect(task?.status).toBe('backlog');
    }).toPass({ timeout: 10000 });
  });

  test('should not show review bar in backlog state', async ({ page }) => {
    const taskId = await createTaskViaApi(page, projectId, `review-hidden-backlog-${Date.now()}`);
    expect(taskId).not.toBe('');

    // Navigate to the task (created in backlog state)
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Review bar should NOT be visible in backlog state
    await expect(page.locator('.chat-review-bar')).not.toBeVisible({ timeout: 5000 });

    // Also verify no review-specific buttons appear
    const doneBtn = page.locator('.btn-move:has-text("Done")');
    const reviewBtn = page.locator('.btn-move:has-text("Review")');
    await expect(doneBtn).not.toBeVisible({ timeout: 3000 });
    await expect(reviewBtn).not.toBeVisible({ timeout: 3000 });
  });
});
