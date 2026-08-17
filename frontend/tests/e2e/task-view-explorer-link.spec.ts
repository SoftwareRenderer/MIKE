import { test, expect } from '@playwright/test';

test.describe('Task View Explorer Link', () => {
  test.describe.configure({ mode: 'serial' });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open explorer with worktree path when clicking project path in task header', async ({ page }) => {
    // Select the global-setup project by exact name. Clicking `.first()` is
    // unreliable in parallel runs: other specs create extra projects (which
    // can sort earlier in the sidebar) and share the same server session, so
    // the selection can be mid-switch and the board can re-render under the
    // click. The global-setup project always has tasks, so this is also the
    // only board where the "first card" below is guaranteed to exist.
    const projectBtn = page.locator('button.project-item').filter({
      has: page.locator('span.project-name', { hasText: /^test-project$/ }),
    });
    if ((await projectBtn.count()) === 0) {
      return; // no test project — nothing to test
    }
    await projectBtn.click();
    await page.waitForSelector('.card', { timeout: 10000 });

    // If no cards exist, skip (nothing to test)
    const cardCount = await page.locator('.card').count();
    if (cardCount === 0) {
      return;
    }

    // Get the task ID from the first card
    const taskId = await page.locator('.card').first().getAttribute('data-task-id');
    if (!taskId) {
      return;
    }

    // Click first card to open task detail
    await page.locator('.card').first().click();
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });

    // Verify the explorer button is visible and clickable.
    // The task detail header has its own "Open explorer" button (a plain
    // icon-btn — the .explorer-btn class only exists on the board's TopBar,
    // which is not rendered on the task detail page).
    const explorerBtn = page.locator('.task-detail-page').getByRole('button', { name: 'Open explorer' });
    await expect(explorerBtn).toBeVisible();

    // Click the explorer button
    await explorerBtn.click();

    // Verify explorer view opens
    await expect(page.locator('.explorer-view')).toBeVisible({ timeout: 10000 });

    // Verify the explorer shows the task's worktree path (contains task ID)
    const explorerPath = page.locator('.explorer-view .project-path');
    await expect(explorerPath).toBeVisible();
    const pathText = await explorerPath.textContent();
    expect(pathText).toContain(taskId);
  });
});
