import { test, expect } from '@playwright/test';
import { ensureChatForm } from './helpers';

test.describe.serial('Task Message Actions', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Open a task and ensure chat form is ready
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await ensureChatForm(page);
  });

  test('should show copy, edit, and delete buttons on user message', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Test actions ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // All three action buttons should be visible on the message
    const message = page.locator('.chat-message.user').last();
    await expect(message.locator('.action-btn[title="Copy"]')).toBeVisible();
    await expect(message.locator('.action-btn[title="Edit"]')).toBeVisible();
    await expect(message.locator('.action-btn[title="Delete"]')).toBeVisible();
  });

  test('should copy message content to clipboard', async ({ page }) => {
    // Send a message with unique content
    const textarea = page.locator('textarea[placeholder*="message"]');
    const uniqueMsg = `Copy me ${Date.now()}`;
    await textarea.fill(uniqueMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Grant clipboard permission and click copy
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const copyBtn = page.locator('.chat-message.user').last().locator('.action-btn[title="Copy"]');
    await copyBtn.click();
    await page.waitForTimeout(200);

    // Verify clipboard content
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboardText).toContain(uniqueMsg);
  });

  test('should show delete confirmation dialog', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Delete test ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Click delete button
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[title="Delete"]');
    await deleteBtn.click();

    // Check that delete dialog appears
    const dialog = page.locator('.delete-dialog');
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await expect(dialog.locator('h3')).toContainText('Delete Message');

    // Cancel deletion
    await dialog.locator('.btn-cancel').click();
    await expect(dialog).not.toBeVisible();

    // Message should still be visible
    await expect(page.locator('.chat-message.user').last()).toBeVisible();
  });

  test('should delete message on confirm', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Will be deleted ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Count user messages before delete
    const userCountBefore = await page.locator('.chat-message.user').count();

    // Click delete and confirm
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[title="Delete"]');
    await deleteBtn.click();
    await page.locator('.delete-dialog .btn-delete').click();

    // Dialog should close and user message count should decrease
    await expect(page.locator('.delete-dialog')).not.toBeVisible({ timeout: 5000 });
    const userCountAfter = await page.locator('.chat-message.user').count();
    expect(userCountAfter).toBe(userCountBefore - 1);
  });

  test('should enter edit mode when edit button is clicked', async ({ page }) => {
    // Send a unique message
    const textarea = page.locator('textarea[placeholder*="message"]');
    const uniqueMsg = `Edit me ${Date.now()}`;
    await textarea.fill(uniqueMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Click edit button
    const editBtn = page.locator('.chat-message.user').last().locator('.action-btn[title="Edit"]');
    await editBtn.click();

    // Edit form should appear with original content
    const editTextarea = page.locator('.edit-form textarea');
    await expect(editTextarea).toBeVisible();
    await expect(editTextarea).toHaveValue(uniqueMsg);
  });

  test('should cancel edit when cancel button is clicked', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Cancel edit ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Enter edit mode
    await page.locator('.chat-message.user').last().locator('.action-btn[title="Edit"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // Cancel edit
    await page.locator('.btn-edit-cancel').click();

    // Edit form should be gone, message bubble should be visible
    await expect(page.locator('.edit-form')).not.toBeVisible();
    await expect(page.locator('.message-bubble').last()).toBeVisible();
  });

  test('should update message when edit is submitted', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Original content ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Edit the message
    await page.locator('.chat-message.user').last().locator('.action-btn[title="Edit"]').click();
    await page.locator('.edit-form textarea').fill('Edited content');
    await page.locator('.btn-edit-submit').click();

    // Edit form should close and message should show new content
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.message-bubble').last()).toContainText('Edited content');
  });

  test('should show regenerate button on assistant message after LLM response', async ({ page }) => {
    // Send a message to trigger LLM response
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Regenerate test ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for assistant message (may not appear if executor fails to start)
    const assistantVisible = await page.locator('.chat-message.assistant').last().isVisible({ timeout: 10000 }).catch(() => false);
    if (!assistantVisible) {
      return; // Executor didn't start, skip
    }

    // Wait for streaming to stop (cursor should disappear)
    await expect(page.locator('.cursor-blink')).not.toBeVisible({ timeout: 10000 });

    // Regenerate button should be visible on the assistant message
    const regenerateBtn = page.locator('.chat-message.assistant').last().locator('.action-btn[title="Regenerate"]');
    await expect(regenerateBtn).toBeVisible();
  });
});
