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
    await expect(message.locator('.action-btn[aria-label*="Copy"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="Edit"]')).toBeVisible();
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
    const editBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Edit"]');
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
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Edit"]').click();
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
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Edit"]').click();
    await page.locator('.edit-form textarea').fill('Edited content');
    await page.locator('.btn-edit-submit').click();

    // Edit form should close and message should show new content (optimistic update)
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });
    await expect(page.locator('.message-bubble').last()).toContainText('Edited content');
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
});

test.describe.serial('Executor Context (task_messages)', () => {
  let taskId: string | null = null;
  let projectId: string | null = null;

  test('should not include deleted messages in task_messages', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a fresh task and open it
    const taskTitle = `context_test_${Date.now()}`;
    await createAndOpenTask(page, taskTitle);

    // Capture IDs
    taskId = (await page.url()).match(/task=([^&]+)/)?.[1] || null;
    projectId = await getProjectId(page);
    expect(taskId).toBeTruthy();
    expect(projectId).toBeTruthy();

    // Wait for chat form to be ready
    await ensureChatForm(page, 15000);

    const textarea = page.locator('textarea[placeholder*="message"]');

    // Send first message
    const firstMsg = `first_message_${Date.now()}`;
    await textarea.fill(firstMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await page.waitForTimeout(2000);

    // Verify first message is in task_messages via API
    let apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    let messages = await apiRes.json() as any[];
    const firstMsgInDb = messages.find((m: any) => m.role === 'user' && m.content === firstMsg);
    expect(firstMsgInDb, 'First message should be in task_messages').toBeTruthy();

    // Delete the first message
    const userMsgs = page.locator('.chat-message.user');
    await expect(userMsgs.last()).toBeVisible({ timeout: 10000 });
    await userMsgs.last().locator('.action-btn[aria-label*="Delete"]').click();
    await page.locator('.delete-dialog .btn-delete').click();
    await expect(page.locator('.delete-dialog')).not.toBeVisible({ timeout: 5000 });
    await page.waitForTimeout(1000);

    // Verify first message is removed from task_messages via API
    apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    messages = await apiRes.json() as any[];
    const deletedMsgInDb = messages.find((m: any) => m.role === 'user' && m.content === firstMsg);
    expect(deletedMsgInDb, 'Deleted message should NOT be in task_messages').toBeFalsy();

    // Send a second message
    await textarea.fill(`second_message_${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await page.waitForTimeout(2000);

    // Verify task_messages has the second message but NOT the deleted first message
    apiRes = await page.request.get(`http://localhost:3000/api/projects/${projectId}/tasks/${taskId}/messages`);
    messages = await apiRes.json() as any[];
    const userMessagesInDb = messages.filter((m: any) => m.role === 'user');
    const hasDeleted = userMessagesInDb.some((m: any) => m.content === firstMsg);
    const hasSecond = userMessagesInDb.some((m: any) => m.content?.startsWith('second_message_'));
    expect(hasDeleted, 'task_messages should NOT contain deleted message').toBeFalsy();
    expect(hasSecond, 'task_messages should contain second message').toBeTruthy();

    // Reload page and verify the deleted message does not reappear in the chat
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 15000 });
    await page.waitForTimeout(2000);

    const chatContent = await page.locator('.chat-messages').textContent();
    expect(chatContent, 'Chat should NOT show deleted message after reload').not.toContain(firstMsg);
  });
});
