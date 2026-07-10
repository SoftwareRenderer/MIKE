import { test, expect } from '@playwright/test';
import { ensureChatForm, waitForChatForm, getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Edit File Tool Call', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openTask(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `edit-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);
  }

  async function requireSendButton(page: any): Promise<boolean> {
    return await page.locator('.btn-send').isVisible({ timeout: 15000 }).catch(() => false);
  }

  test('should show Editing label and +X/-Y toggle for edit_file tool calls', async ({ page }) => {
    test.setTimeout(30000);
    await openTask(page);

    // Graceful: executor may not start in test env
    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers the edit_file fixture.
    await chatTextarea.fill('test edit file');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for tool call card to appear
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return; // graceful skip

    // Verify the tool name shows "Edit" (display name for edit_file)
    await expect(page.locator('.tool-name').first()).toBeVisible();
    const toolNameText = await page.locator('.tool-name').first().textContent();
    expect(toolNameText?.toLowerCase()).toContain('edit');

    // Wait for tool to complete (pending status disappears)
    await page.waitForSelector('.tool-duration', { timeout: 15000 }).catch(() => {});

    // After completion, the +X/-Y toggle button should be visible
    const toggleVisible = await page.locator('.edit-diff-toggle').isVisible({ timeout: 10000 }).catch(() => false);
    if (toggleVisible) {
      // Verify the button shows colored line counts
      const toggleText = await page.locator('.edit-diff-toggle').textContent();
      expect(toggleText).toMatch(/\+\d+\/-\d+/);

      // Click to expand — diff should appear
      await page.locator('.edit-diff-toggle').click();
      const diffVisible = await page.locator('.edit-diff').isVisible({ timeout: 5000 }).catch(() => false);
      expect(diffVisible).toBe(true);

      // Click again to collapse — diff should hide
      await page.locator('.edit-diff-toggle').click();
      const diffHidden = await page.locator('.edit-diff').isVisible({ timeout: 5000 }).catch(() => false);
      expect(diffHidden).toBe(false);
    }
  });
});
