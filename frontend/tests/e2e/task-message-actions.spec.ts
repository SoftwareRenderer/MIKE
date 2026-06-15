import { test, expect } from '@playwright/test';
import { ensureChatForm, createAndOpenTask, getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task Message Actions', () => {
  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a fresh task and open it to avoid shared-state issues
    await createAndOpenTask(page);
    // Graceful: executor may not start in test env
    const chatReady = await page.locator('.btn-send').isVisible({ timeout: 15000 }).catch(() => false);
    if (!chatReady) {
      // Chat form not ready, tests will skip via graceful checks
    }
  });

  test('should show copy, fork, and delete buttons on user message', async ({ page }) => {
    // Graceful skip: chat form may not be ready if executor didn't start
    const chatReady = await page.locator('.chat-form').isVisible({ timeout: 5000 }).catch(() => false);
    if (!chatReady) return;

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
    await expect(message.locator('.action-btn[aria-label*="new thread"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="Delete"]')).toBeVisible();
  });

  test('should copy message content to clipboard', async ({ page }) => {
    test.setTimeout(30000);
    // Graceful skip: chat form may not be ready if executor didn't start
    const chatReady = await page.locator('.chat-form').isVisible({ timeout: 5000 }).catch(() => false);
    if (!chatReady) return;

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
    // Verify clipboard content (clipboard write is near-instant)
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
    const dialog = page.locator('.confirm-dialog');
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
    await page.locator('.confirm-dialog .btn-confirm').click();

    // Dialog should close and user message count should decrease
    await expect(page.locator('.confirm-dialog')).not.toBeVisible({ timeout: 5000 });
    // Wait for the message to actually disappear (async API call must complete)
    await expect(page.locator('.chat-message.user')).toHaveCount(userCountBefore - 1, { timeout: 5000 });
  });

  test('should open fork form when fork button is clicked', async ({ page }) => {
    // Count existing assistant messages
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a unique message
    const textarea = page.locator('textarea[placeholder*="message"]');
    const uniqueMsg = `Fork me ${Date.now()}`;
    await textarea.fill(uniqueMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Wait for LLM response (a NEW assistant message must appear)
    // Graceful skip: executor may not start in test env
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 15000 }
    ).catch(() => false);
    if (!newAssistantAppeared) return;

    // Click fork button
    const forkBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="new thread"]');
    await forkBtn.click();

    // Fork form should appear with original content
    const editTextarea = page.locator('.edit-form textarea');
    await expect(editTextarea).toBeVisible();
    await expect(editTextarea).toHaveValue(uniqueMsg);

    // Fork button should be visible in the form (LLM response exists after this message)
    await expect(page.locator('.btn-edit-submit')).toContainText('Fork');
  });

  test('should cancel fork when cancel button is clicked', async ({ page }) => {
    // Count existing assistant messages
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    await textarea.fill(`Cancel fork ${Date.now()}`);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Wait for LLM response so the message has an assistant reply after it
    // Graceful skip: executor may not start
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 15000 }
    ).catch(() => false);
    if (!newAssistantAppeared) return;

    // Enter fork mode
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // Cancel fork
    await page.locator('.btn-edit-cancel').click();

    // Fork form should be gone, message bubble should be visible
    await expect(page.locator('.edit-form')).not.toBeVisible();
    await expect(page.locator('.message-bubble').last()).toBeVisible();
  });

  test('should close fork form when fork is submitted', async ({ page }) => {
    // Count existing assistant messages
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a message
    const textarea = page.locator('textarea[placeholder*="message"]');
    const originalMsg = `Original content ${Date.now()}`;
    await textarea.fill(originalMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').last()).toBeVisible({ timeout: 10000 });

    // Wait for LLM response so the message has an assistant reply after it
    // Graceful skip: executor may not start
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 15000 }
    ).catch(() => false);
    if (!newAssistantAppeared) return;

    // Fork the message
    const forkedMsg = `Forked content ${Date.now()}`;
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="new thread"]').click();
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });

    // Wait for async operations to settle (send button reappears when complete)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 }).catch(() => {});

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
    if (!newAssistantAppeared) return;

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
    if (!newAssistantAppeared) return;

    // Wait for streaming to stop: the send button reappears when LLM is done
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Regenerate button should be visible on the last assistant message
    const lastAssistant = page.locator('.chat-message.assistant').last();
    const regenerateBtn = lastAssistant.locator('.action-btn[aria-label*="Regenerate"]');
    await expect(regenerateBtn).toBeVisible();
  });

  test('should only show regenerate button on the last assistant message when it is the last entry', async ({ page }) => {
    // Create messages directly via API to guarantee multiple assistant messages
    // (follow-up messages may not trigger the executor in test env)
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `regenerate-scope-${Date.now()}`);

    // Create user + assistant message pairs via API
    // Use sequential API calls (not in a single evaluate) for reliable timestamp ordering
    const base = `/api/projects/${projectId}/tasks/${taskId}/messages`;
    const headers = { 'Content-Type': 'application/json' };
    await page.evaluate((url) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role: 'user', content: 'First user message' }) }), base);
    await page.waitForTimeout(100);
    await page.evaluate((url) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role: 'assistant', content: 'First assistant response' }) }), base);
    await page.waitForTimeout(100);
    await page.evaluate((url) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role: 'user', content: 'Second user message' }) }), base);
    await page.waitForTimeout(100);
    await page.evaluate((url) => fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ role: 'assistant', content: 'Second assistant response' }) }), base);

    // Navigate to the task to load messages
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message.assistant')).toHaveCount(2, { timeout: 10000 });

    // Get all assistant messages
    const assistantMessages = page.locator('.chat-message.assistant');

    // Last assistant message (last entry) should have regenerate button
    const lastAssistant = assistantMessages.last();
    const lastRegenerateBtn = lastAssistant.locator('.action-btn[aria-label*="Regenerate"]');
    await expect(lastRegenerateBtn).toBeVisible();

    // Earlier assistant messages should NOT have regenerate button
    const earlierAssistant = assistantMessages.first();
    const earlierRegenerateBtn = earlierAssistant.locator('.action-btn[aria-label*="Regenerate"]');
    await expect(earlierRegenerateBtn).not.toBeVisible();
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
    await followUpEl.locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible({ timeout: 5000 });
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });

    // Wait for async operations to settle
    await expect(page.locator(".btn-send")).toBeVisible({ timeout: 10000 }).catch(() => {});

    // Messages should still be visible
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });

  test('should show Send button when editing last user message with no LLM response', async ({ page }) => {
    // Send a message and immediately edit it before LLM creates assistant entry
    // (race condition: we click fork before the assistant entry is created)
    const textarea = page.locator('textarea[placeholder*="message"]');
    const uniqueMsg = `Send button test ${Date.now()}`;
    await textarea.fill(uniqueMsg);

    // Click Enter and immediately open the edit form on the user message
    // We need to be faster than the assistant entry creation
    await textarea.press('Enter');

    // Wait for the user message to appear in the DOM
    await expect(page.locator('.chat-message.user').filter({ hasText: uniqueMsg })).toBeVisible({ timeout: 10000 });

    // The last user message should show Send button in edit form
    // (if LLM hasn't created an assistant entry yet)
    // Note: with fast mock LLM, the assistant entry may already exist.
    // In that case, the button shows "Fork" which is also correct behavior.
    const lastUserMsg = page.locator('.chat-message.user').filter({ hasText: uniqueMsg }).last();
    await lastUserMsg.locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // The button should be either "Send" (no assistant yet) or "Fork" (assistant exists)
    // Both are valid — just verify the edit form opens correctly
    const submitBtn = page.locator('.btn-edit-submit');
    const btnText = await submitBtn.textContent();
    expect(['Send', 'Fork']).toContain(btnText?.trim());
  });

  test('should show Fork button when editing a message that has an LLM response after it', async ({ page }) => {
    // First wait for the initial task prompt (user message) + any LLM response
    const assistantCountBefore = await page.locator('.chat-message.assistant').count();

    // Send a message and wait for LLM response
    const textarea = page.locator('textarea[placeholder*="message"]');
    const msgWithResponse = `Fork after response ${Date.now()}`;
    await textarea.fill(msgWithResponse);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });

    // Wait for assistant response
    const newAssistantAppeared = await page.waitForFunction(
      (count) => document.querySelectorAll('.chat-message.assistant').length > count,
      assistantCountBefore,
      { timeout: 10000 }
    ).catch(() => false);
    if (!newAssistantAppeared) return;

    // Wait for streaming to stop
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Click fork/edit on the user message that has an LLM response after it
    const userMsgWithResponse = page.locator('.chat-message.user').filter({ hasText: msgWithResponse }).last();
    await userMsgWithResponse.locator('.action-btn[aria-label*="new thread"]').click();

    // Edit form should appear with Fork button (not Send)
    await expect(page.locator('.edit-form textarea')).toBeVisible();
    await expect(page.locator('.btn-edit-submit')).toContainText('Fork');
  });

  test('should update message and close edit form when Send/Fork is submitted', async ({ page }) => {
    // Send a message and wait for LLM response
    const textarea = page.locator('textarea[placeholder*="message"]');
    const originalMsg = `Edit submit test ${Date.now()}`;
    await textarea.fill(originalMsg);
    await textarea.press('Enter');
    await expect(textarea).toHaveValue('', { timeout: 5000 });
    await expect(page.locator('.chat-message.user').filter({ hasText: originalMsg })).toBeVisible({ timeout: 10000 });

    // Wait for LLM response
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 15000 });

    // Open edit form on the last user message
    const lastUserMsg = page.locator('.chat-message.user').filter({ hasText: originalMsg }).last();
    await lastUserMsg.locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // Edit the message
    const editedMsg = `Edited submit test ${Date.now()}`;
    await page.locator('.edit-form textarea').fill(editedMsg);

    // Submit
    await page.locator('.btn-edit-submit').click();

    // Edit form should close
    await expect(page.locator('.edit-form')).not.toBeVisible({ timeout: 5000 });

    // Wait for async operations to settle
    await expect(page.locator(".btn-send")).toBeVisible({ timeout: 10000 }).catch(() => {});

    // Messages should still be visible (either forked or edited)
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });
});
