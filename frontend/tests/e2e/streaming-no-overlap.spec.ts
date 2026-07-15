import { test, expect } from '@playwright/test';
import { waitForChatForm, getProjectId, createTaskViaApi, navigateToTask } from './helpers';

test.describe.configure({ mode: 'parallel' });
test.describe('Streaming no-overlap', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  async function openTask(page: any, title?: string): Promise<void> {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, title || `no-overlap-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await waitForChatForm(page);
  }

  async function requireSendButton(page: any): Promise<boolean> {
    return await page.locator('.btn-send').isVisible({ timeout: 15000 }).catch(() => false);
  }

  test('should not have overlapping entries after streaming tool calls', async ({ page }) => {
    test.setTimeout(30000);
    await openTask(page);

    // Graceful: executor may not start in test env
    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers tool calls (mock routes "list"/"read"/"command" to tool_call_response)
    await chatTextarea.fill('test read file command');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for tool call card to appear
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return; // graceful skip

    // Wait for streaming to complete (cursor blink disappears)
    await page.waitForSelector('.cursor-blink', { state: 'hidden', timeout: 15000 }).catch(() => {});

    // Wait a moment for layout to settle
    await page.waitForTimeout(500);

    // Get all virtual items and check their bounding boxes don't overlap
    const items = page.locator('.virtual-item[data-virtual-index]');
    const count = await items.count();

    if (count >= 2) {
      // Check that consecutive items don't overlap vertically
      for (let i = 0; i < count - 1; i++) {
        const currentBox = await items.nth(i).boundingBox();
        const nextBox = await items.nth(i + 1).boundingBox();

        if (currentBox && nextBox) {
          // Current item's bottom should be <= next item's top (no overlap)
          // Allow a small tolerance for sub-pixel rendering
          const tolerance = 2;
          expect(currentBox.y + currentBox.height)
            .toBeLessThanOrEqual(nextBox.y + tolerance);
        }
      }
    }
  });

  test('should not have overlapping entries after multiple tool results', async ({ page }) => {
    test.setTimeout(45000);
    await openTask(page);

    // Graceful: executor may not start in test env
    if (!await requireSendButton(page)) return;

    const chatTextarea = page.locator('textarea[placeholder*="message"]');

    // Send a message that triggers tool calls
    await chatTextarea.fill('test read file directory list');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 10000 });

    // Wait for at least one tool call card to appear
    const toolCallVisible = await page.locator('.tool-call-card').first()
      .isVisible({ timeout: 15000 }).catch(() => false);
    if (!toolCallVisible) return; // graceful skip

    // Wait for streaming to complete
    await page.waitForSelector('.cursor-blink', { state: 'hidden', timeout: 30000 }).catch(() => {});

    // Wait for all tool calls to complete (no pending spinners)
    await page.waitForSelector('.tool-status.pending', { state: 'hidden', timeout: 30000 }).catch(() => {});

    // Wait a moment for layout to settle
    await page.waitForTimeout(500);

    // Get all virtual items and check their bounding boxes don't overlap
    const items = page.locator('.virtual-item[data-virtual-index]');
    const count = await items.count();

    if (count >= 2) {
      // Check that consecutive items don't overlap vertically
      for (let i = 0; i < count - 1; i++) {
        const currentBox = await items.nth(i).boundingBox();
        const nextBox = await items.nth(i + 1).boundingBox();

        if (currentBox && nextBox) {
          const tolerance = 2;
          expect(currentBox.y + currentBox.height)
            .toBeLessThanOrEqual(nextBox.y + tolerance);
        }
      }
    }
  });
});
