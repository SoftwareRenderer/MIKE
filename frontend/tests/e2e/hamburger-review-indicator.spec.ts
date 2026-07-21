import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

// ============================================================
// Hamburger Review Indicator (Mobile)
// ============================================================

test.describe.serial('Hamburger Review Indicator', () => {
  test.beforeEach(async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show yellow dot on hamburger when a task is in review', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Initially no tasks in review — hamburger should NOT have a dot
    const hamburger = page.locator('.hamburger');
    await expect(hamburger).toBeVisible();

    // Check no review indicator initially (no tasks in review)
    const initialDot = await page.locator('.hamburger .review-dot').count();
    expect(initialDot).toBe(0);

    // Create a task and move it to review
    const taskId = await createTaskViaApi(page, projectId, `review-indicator-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // Wait for the review dot to appear on the hamburger button
    await expect(page.locator('.hamburger .review-dot')).toBeVisible({ timeout: 10000 });
  });

  test('should hide yellow dot when no tasks are in review', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Move ALL existing tasks out of review (cleanup from previous serial tests)
    const allTasks = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.filter((t: any) => t.status === 'review');
    }, projectId);

    for (const task of allTasks) {
      await csrfFetch(page, `/api/projects/${projectId}/tasks/${task.id}/move`, {
        method: 'POST',
        body: { status: 'backlog' },
      });
    }

    // Wait for the dot to disappear (all tasks moved out of review)
    await expect(async () => {
      const dotVisible = await page.locator('.hamburger .review-dot').isVisible();
      expect(dotVisible).toBe(false);
    }).toPass({ timeout: 15000 });
  });

  test('should not show review dot on desktop', async ({ page }) => {
    // Reset to desktop viewport
    await page.setViewportSize({ width: 1280, height: 720 });
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `review-indicator-desktop-${Date.now()}`);
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'review' },
    });

    // On desktop, the hamburger is hidden, so the dot should not be visible
    const hamburger = page.locator('.hamburger');
    const isVisible = await hamburger.isVisible().catch(() => false);
    expect(isVisible).toBe(false);
  });
});
