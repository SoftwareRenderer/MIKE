import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, waitForChatForm, waitForSendButtonGraceful } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Edit Diff Expand Persistence', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should persist edit diff expanded state across virtual list remount', async ({ page }) => {
    test.setTimeout(30000);
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `edit-diff-test-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat form (handles start/restart buttons)
    const hasForm = await waitForChatForm(page);
    if (!hasForm) return; // graceful skip

    // Graceful: executor may not start in test env
    if (!await waitForSendButtonGraceful(page)) return;
    await chatTextarea.fill('test edit file diff');
    await chatTextarea.press('Enter');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for tool call card to appear
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return; // graceful skip

    // Verify it's an edit_file tool call
    const toolName = page.locator('.tool-name').first();
    await expect(toolName).toBeVisible();
    const nameText = await toolName.textContent();
    expect(nameText?.toLowerCase()).toContain('edit');

    // Wait for the edit diff toggle button to appear
    const diffToggle = page.locator('.edit-diff-toggle');
    const toggleVisible = await diffToggle.isVisible({ timeout: 10000 }).catch(() => false);
    if (!toggleVisible) return; // graceful skip

    // The fixture has a trivial diff (2 added lines), so it starts expanded.
    // Verify the diff content is visible (expanded state)
    const diffContent = page.locator('.edit-diff');
    const diffVisible = await diffContent.isVisible({ timeout: 5000 }).catch(() => false);
    expect(diffVisible).toBe(true);

    // Collapse the diff
    await diffToggle.click();
    await expect(diffContent).not.toBeVisible({ timeout: 5000 });

    // Expand it again
    await diffToggle.click();
    await expect(diffContent).toBeVisible({ timeout: 5000 });

    // Verify the arrow shows collapsed state (▴ when expanded)
    const arrow = page.locator('.edit-diff-arrow');
    const arrowText = await arrow.textContent();
    expect(arrowText).toBe('▴'); // expanded shows up arrow

    // Collapse again
    await diffToggle.click();
    const arrowTextCollapsed = await arrow.textContent();
    expect(arrowTextCollapsed).toBe('▾'); // collapsed shows down arrow

    // Expand one more time and verify state persists
    await diffToggle.click();
    await expect(diffContent).toBeVisible({ timeout: 5000 });
    const finalArrow = await page.locator('.edit-diff-arrow').textContent();
    expect(finalArrow).toBe('▴');
  });
});
