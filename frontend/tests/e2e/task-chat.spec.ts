import { test, expect } from '@playwright/test';

test.describe('Task Chat', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show empty chat state initially', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Should show empty state message
    await expect(page.locator('.chat-empty')).toBeVisible();
    await expect(page.locator('.chat-empty p')).toContainText('No messages yet');
  });

  test('should show send button when typing', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Test message');

    // Send button should be enabled
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).not.toBeDisabled();
  });

  test('should disable send button when empty', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Send button should be disabled when textarea is empty
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeDisabled();
  });

  test('should send message with Enter key', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Enter key test');
    await chatTextarea.press('Enter');

    // Textarea should be cleared
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });
  });

  test('should not send on Shift+Enter', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Line 1');
    await chatTextarea.press('Shift+Enter');
    await chatTextarea.type('Line 2');

    // Textarea should still have content
    await expect(chatTextarea).toHaveValue('Line 1Line 2');
  });

  test('should show WebSocket connection status', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Should show status bar
    await expect(page.locator('.status-bar')).toBeVisible({ timeout: 5000 });
  });

  test('should have diff column with refresh button', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Diff column should show empty state
    await expect(page.locator('.diff-empty')).toBeVisible();
    await expect(page.locator('.diff-empty p')).toContainText('No changes yet');

    // Refresh button should be visible
    await expect(page.locator('.column-diff .btn-icon-sm')).toBeVisible();
  });

  test('should show task list in context column', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Task list should be visible
    await expect(page.locator('.task-list')).toBeVisible();

    // Current task should be selected
    await expect(page.locator('.task-item.selected')).toBeVisible();
  });

  test('should show task actions in header', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Delete button should always be visible
    const deleteButton = page.locator('.header-actions .btn-icon').last();
    await expect(deleteButton).toBeVisible();
  });

  test('should allow editing task title', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Click on title to edit
    await page.click('.title');

    // Title input should appear
    await expect(page.locator('.title-input')).toBeVisible();
  });

  test('should show status badge', async ({ page }) => {
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Badge should be visible
    await expect(page.locator('.badge')).toBeVisible();
  });
});
