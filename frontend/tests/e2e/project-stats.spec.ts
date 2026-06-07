import { test, expect } from './helpers';
import { getProjectId } from './helpers';

test.describe('Project Stats Badges', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should display stat badges for projects with tasks', async ({ page }) => {
    // Get project ID and create a task via API
    const projectId = await getProjectId(page);
    await page.evaluate(async ({ pid }) => {
      await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: 'test task for stats' }),
      });
    }, { pid: projectId });

    // Wait for the board to refresh and stats to appear
    await page.waitForSelector('.stat-badge', { timeout: 5000 });

    // Verify stat badges are visible
    const badges = page.locator('.stat-badge');
    await expect(badges).toHaveCount(3);

    // First badge should show at least 1 (backlog)
    const firstBadge = badges.nth(0);
    const text = await firstBadge.textContent();
    expect(parseInt(text || '0')).toBeGreaterThanOrEqual(1);
  });

  test('should update stats when a task is moved', async ({ page }) => {
    // Get project ID and create a task via API
    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: 'moveable task' }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId });

    // Wait for initial stats to appear
    await page.waitForSelector('.stat-badge', { timeout: 5000 });

    // Read counts directly from API right before the move to avoid parallel test interference
    const countsBefore = await page.evaluate(async () => {
      const res = await fetch('/api/projects/counts');
      return res.json();
    });
    const projectBefore = countsBefore[projectId];

    // Move the task to in_progress via API
    await page.evaluate(async ({ pid, tid }) => {
      await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
    }, { pid: projectId, tid: taskId });

    // Wait for stats to update (WebSocket event)
    await page.waitForTimeout(500);

    // Read counts again from API
    const countsAfter = await page.evaluate(async () => {
      const res = await fetch('/api/projects/counts');
      return res.json();
    });
    const projectAfter = countsAfter[projectId];

    expect(projectAfter.backlog).toBe(projectBefore.backlog - 1);
    expect(projectAfter.in_progress).toBe(projectBefore.in_progress + 1);
  });

  test('should hide stats when sidebar is collapsed', async ({ page }) => {
    // Create a task so stats are visible
    const projectId = await getProjectId(page);
    await page.evaluate(async ({ pid }) => {
      await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: 'collapse test' }),
      });
    }, { pid: projectId });

    await page.waitForSelector('.stat-badge', { timeout: 5000 });

    // Verify stats are visible
    await expect(page.locator('.stat-badge').first()).toBeVisible();

    // Collapse sidebar
    await page.click('.toggle-btn');

    // Wait for collapse animation
    await page.waitForTimeout(300);

    // Stats should be hidden
    const stats = page.locator('.project-stats');
    // Check that the CSS class 'collapsed' is applied and hides stats
    const isVisible = await stats.first().isVisible().catch(() => false);
    // Stats may exist in DOM but should not be visible due to CSS
    if (isVisible) {
      const style = await stats.first().evaluate(el => window.getComputedStyle(el).display);
      expect(style).toBe('none');
    }
  });
});
