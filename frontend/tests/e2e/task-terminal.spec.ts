import { test, expect } from './helpers';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe('Terminal button in task detail', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
  });

  test('should show terminal button in task detail header', async ({ page }) => {
    // Wait for a card and open it
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Terminal button should be visible in header actions
    const terminalBtn = page.locator('[aria-label="Open terminal"]');
    await expect(terminalBtn).toBeVisible();
  });

  test('should open terminal panel when terminal button is clicked', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const terminalBtn = page.locator('[aria-label="Open terminal"]');
    await expect(terminalBtn).toBeVisible();

    // Click terminal button (may be disabled if worktree doesn't exist, so check enabled state)
    const isEnabled = await terminalBtn.isEnabled();
    if (!isEnabled) {
      test.skip();
    }

    await terminalBtn.click();

    // Terminal panel should appear
    await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 5000 });
  });

  test('should disable terminal button when worktree does not exist', async ({ page }) => {
    // Create a fresh task via API (won't have a worktree)
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `terminal-test-${Date.now()}`);

    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // The terminal button should be disabled for a task without a worktree
    const terminalBtn = page.locator('[aria-label="Open terminal"]');
    await expect(terminalBtn).toBeVisible();

    // Check that the worktree-exists endpoint returns false
    const exists = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/worktree-exists`);
      const data = await res.json();
      return data.exists;
    }, { pid: projectId, tid: taskId });

    if (!exists) {
      // Worktree doesn't exist, button should be disabled
      await expect(terminalBtn).toBeDisabled();
    } else {
      // Worktree exists (e.g., from a previous run), button should be enabled
      await expect(terminalBtn).toBeEnabled();
    }
  });
});
