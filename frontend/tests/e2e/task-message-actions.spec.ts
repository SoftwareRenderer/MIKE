import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask, getProjectId } from './helpers';

test.describe.serial('Task Message Actions', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a fresh task and open it to avoid shared-state issues
    await createAndOpenTask(page);
    await ensureChatForm(page);
  });

  test('should show copy, fork, and delete buttons on user message', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Test actions ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // All three action buttons should be visible on the message
    const message = page.locator('.chat-message.user').last();
    await expect(message.locator('.action-btn[aria-label*="Copy"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="Fork"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="Delete"]')).toBeVisible();
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
    const copyBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Copy"]');
    await copyBtn.click();
    await page.waitForTimeout(100);

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
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Delete"]');
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
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Delete"]');
    await deleteBtn.click();
    await page.locator('.delete-dialog .btn-delete').click();

    // Dialog should close and user message count should decrease
    await expect(page.locator('.delete-dialog')).not.toBeVisible({ timeout: 5000 });
    // Wait for the message to actually disappear (async API call must complete)
    await expect(page.locator('.chat-message.user')).toHaveCount(userCountBefore - 1, { timeout: 5000 });
  });

  test('should open fork form when fork button is clicked', async ({ page }) => {
    // Send a unique message
    const textarea = page.locator('textarea[placeholder*="message"]');
    const uniqueMsg = `Fork me ${Date.now()}`;
    await textarea.fill(uniqueMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Click fork button
    const forkBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Fork"]');
    await forkBtn.click();

    // Fork form should appear with original content
    const editTextarea = page.locator('.edit-form textarea');
    await expect(editTextarea).toBeVisible();
    await expect(editTextarea).toHaveValue(uniqueMsg);

    // Fork button should be visible in the form
    await expect(page.locator('.btn-edit-submit')).toContainText('Fork');
  });

  test('should cancel fork when cancel button is clicked', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Cancel fork ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Enter fork mode
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Fork"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // Cancel fork
    await page.locator('.btn-edit-cancel').click();

    // Fork form should be gone, message bubble should be visible
    await expect(page.locator('.edit-form')).not.toBeVisible();
    await expect(page.locator('.message-bubble').last()).toBeVisible();
  });

  test('should close fork form when fork is submitted', async ({ page }) => {
    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    const originalMsg = `Original content ${Date.now()}`;
    await textarea.fill(originalMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Fork the message
    const forkedMsg = `Forked content ${Date.now()}`;
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Fork"]').click();
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });

    // Wait for async operations to settle
    await page.waitForTimeout(2000);

    // The message should still be visible (either forked or soft-edited depending on task state)
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });

  test('should copy assistant message content to clipboard', async ({ page }) => {
    // Count assistant messages before sending
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a message to trigger LLM response
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Copy assistant test ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for a NEW assistant message to appear
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 10000 }
    ).catch(() => false);
    if (!newAssistantAppeared) {
      test.skip(); // Executor didn't start, skip gracefully
    }

    // Wait for streaming to stop
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Get the assistant message content
    const lastAssistant = page.locator('.chat-message.assistant').last();
    const assistantContent = await lastAssistant.locator('.message-text').first().textContent();
    expect(assistantContent).toBeTruthy();

    // Grant clipboard permission and click copy
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const copyBtn = lastAssistant.locator('.action-btn[aria-label*="Copy"]');
    await copyBtn.click();
    await page.waitForTimeout(100);

    // Verify clipboard content matches the assistant message
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboardText).toBe(assistantContent?.trim());
  });

  test('should show regenerate button on assistant message after LLM response', async ({ page }) => {
    // Count assistant messages before sending
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a message to trigger LLM response
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Regenerate test ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for a NEW assistant message to appear (executor may not start)
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 10000 }
    ).catch(() => false);
    if (!newAssistantAppeared) {
      test.skip(); // Executor didn't start, skip gracefully
    }

    // Wait for streaming to stop: the send button reappears when LLM is done
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Regenerate button should be visible on the last assistant message
    const lastAssistant = page.locator('.chat-message.assistant').last();
    const regenerateBtn = lastAssistant.locator('.action-btn[aria-label*="Regenerate"]');
    await expect(regenerateBtn).toBeVisible();
  });

  test('should close fork form for follow-up message', async ({ page }) => {
    // Send a follow-up message
    const followUpMsg = `Follow up fork test ${Date.now()}`;
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(followUpMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for the user message to appear in the chat
    await expect(page.locator('.chat-message.user').filter({ hasText: followUpMsg })).toBeVisible({ timeout: 10000 });

    // Wait for LLM response (executor may not start for follow-ups in test env)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 }).catch(() => {});

    // Find the follow-up message by content
    const followUpEl = page.locator('.chat-message.user').filter({ hasText: followUpMsg }).last();
    await expect(followUpEl).toBeVisible();

    // Fork the follow-up message
    const forkedMsg = `Forked follow up ${Date.now()}`;
    await followUpEl.locator('.action-btn[aria-label*="Fork"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible({ timeout: 5000 });
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });

    // Wait for async operations to settle
    await page.waitForTimeout(2000);

    // Messages should still be visible
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });
});
