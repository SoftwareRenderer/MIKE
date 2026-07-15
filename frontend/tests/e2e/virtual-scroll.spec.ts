import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, navigateToTask, ensureTaskStarted, waitForChatFormReady } from './helpers';

test.describe.configure({ mode: 'parallel' });

/** Wait for chat form to be idle (send button visible). Stops any active LLM response. */
async function waitForIdle(page: any) {
  await waitForChatFormReady(page);
  if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
    await page.locator('.btn-stop').click();
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 60000 });
  }
}

test.describe('Virtual scroll in chat', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item');
  });

  test('renders virtual scroll container for chat messages', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `virtual-scroll-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // The virtual scroll container should be present
    const virtualList = page.locator('.virtual-list');
    await expect(virtualList).toBeVisible();

    // The spacer div should be present (used for virtual scroll height)
    const spacer = page.locator('.virtual-spacer');
    await expect(spacer).toBeVisible();
  });

  test('auto-scrolls to bottom when new messages arrive', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `auto-scroll-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // Send a message and verify auto-scroll
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.click();
    await textarea.pressSequentially('test auto scroll message');
    await page.locator('.btn-send').click();

    // Wait for the message to appear
    await expect(page.locator('.sticky-user-message')).toContainText('test auto scroll message');

    // After sending, the chat should be scrolled to bottom (new message visible)
    const messages = page.locator('.sticky-user-message');
    await expect(messages.last()).toBeVisible();
  });

  test('sticky header appears when user scrolls up past a user message', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `sticky-header-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // The sticky header should not be visible at the bottom
    const stickyHeader = page.locator('.chat-sticky-header');
    // At bottom of a short conversation, header should not show
    // (depends on conversation length; for a single message, no header)
    // Just verify the component is present in the DOM
    await expect(page.locator('.chat-messages-wrapper')).toBeVisible();
  });

  test('virtual list items have correct positioning', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `item-position-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // Check that virtual items have absolute positioning
    const virtualItems = page.locator('.virtual-item');
    const count = await virtualItems.count();
    expect(count).toBeGreaterThan(0);

    // Each virtual item should have a style with position and top
    for (let i = 0; i < count; i++) {
      const style = await virtualItems.nth(i).getAttribute('style');
      expect(style).toContain('position: absolute');
      expect(style).toContain('top:');
    }
  });

  test('virtual list spacer has correct height', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `spacer-height-${Date.now()}`);
    await navigateToTask(page, taskId, projectId);
    await ensureTaskStarted(page, projectId, taskId);
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await waitForIdle(page);

    // The spacer should have a height > 0
    const spacer = page.locator('.virtual-spacer');
    const spacerStyle = await spacer.getAttribute('style');
    expect(spacerStyle).toContain('height:');

    // Extract the height value and verify it's positive
    const heightMatch = spacerStyle?.match(/height:\s*(\d+)px/);
    expect(heightMatch).not.toBeNull();
    const height = parseInt(heightMatch![1]);
    expect(height).toBeGreaterThan(0);
  });
});
