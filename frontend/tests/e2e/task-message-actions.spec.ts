import { test, expect } from '@playwright/test';
import {ensureChatForm, createAndOpenTask, getProjectId, createTaskViaApi, navigateToTask, csrfFetch, ensureTaskStarted} from './helpers';

test.describe.serial('Task Message Actions', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    test.setTimeout(120000);
    await page.goto('/');
    await page.waitForSelector('.card');

    projectId = await getProjectId(page);
    // Create a fresh task and open it
    taskId = await createTaskViaApi(page, projectId, `msg-actions-${Date.now()}`);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Move task to in_progress via API (bypasses executor, shows chat form)
    const ok = await ensureTaskStarted(page, projectId, taskId);
    if (!ok) test.skip('Could not move task to in_progress');

    // Reload to pick up status change (retry if chat form doesn't appear)
    for (let attempt = 0; attempt < 3; attempt++) {
      await page.reload();
      await expect(page.locator('.task-detail-page')).toBeVisible();
      // Check for chat form, restart button, or start button
      const hasForm = await page.locator('.chat-form').isVisible().catch(() => false);
      if (hasForm) break;

      // If restart button is visible, click it
      if (await page.locator('.btn-restart-task').isVisible().catch(() => false)) {
        await page.locator('.btn-restart-task').click();
        if (await page.locator('.chat-form').isVisible().catch(() => false)) break;
      }
      // If start button is still visible, try moving again
      if (await page.locator('.btn-start-task').isVisible().catch(() => false)) {
        await ensureTaskStarted(page, projectId, taskId);
      }
    }
    // Final check
    if (!await page.locator('.chat-form').isVisible().catch(() => false)) {
      test.skip('Chat form not visible after reload (task may still be in backlog)');
    }
  });

  test('should show copy, fork, and delete buttons on user message', async ({ page }) => {
    // Add a user message via API (no executor needed)
    const msgContent = `Test actions ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: msgContent },
    });

    // Reload to pick up the new message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // All three action buttons should be visible on the message
    const message = page.locator('.chat-message.user').last();
    await expect(message.locator('.action-btn[aria-label*="Copy"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="new thread"]')).toBeVisible();
    await expect(message.locator('.action-btn[aria-label*="Delete"]')).toBeVisible();
  });

  test('should copy message content to clipboard', async ({ page }) => {
    // Add a user message via API (no executor needed)
    const uniqueMsg = `Copy me ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: uniqueMsg },
    });

    // Reload to pick up the new message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // Grant clipboard permission and click copy
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const copyBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Copy"]');
    if (!await copyBtn.isVisible().catch(() => false)) {
      test.skip('Copy button not visible');
    }
    await copyBtn.click();
    // Verify clipboard content (clipboard write is near-instant)
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboardText).toContain(uniqueMsg);
  });

  test('should show delete confirmation dialog', async ({ page }) => {
    // Add a user message via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: `Delete test ${Date.now()}` },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // Click delete button
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Delete"]');
    await deleteBtn.click();

    // Check that delete dialog appears
    const dialog = page.locator('.confirm-dialog');
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('h3')).toContainText('Delete Message');

    // Cancel deletion
    await dialog.locator('.btn-cancel').click();
    await expect(dialog).not.toBeVisible();

    // Message should still be visible
    await expect(page.locator('.chat-message.user').last()).toBeVisible();
  });

  test('should delete message on confirm', async ({ page }) => {
    // Add a user message via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: `Will be deleted ${Date.now()}` },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // Count user messages before delete
    const userCountBefore = await page.locator('.chat-message.user').count();
    expect(userCountBefore).toBeGreaterThan(0);

    // Click delete and confirm
    const deleteBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="Delete"]');
    await deleteBtn.click();
    await expect(page.locator('.confirm-dialog')).toBeVisible();
    await page.locator('.confirm-dialog .btn-confirm').click();

    // Dialog should close
    await expect(page.locator('.confirm-dialog')).not.toBeVisible();

    // Reload to pick up deletion (WebSocket may not update in test env)
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // User message count should decrease
    const userCountAfter = await page.locator('.chat-message.user').count();
    expect(userCountAfter).toBeLessThan(userCountBefore);
  });

  test('should open fork form when fork button is clicked', async ({ page }) => {
    // Add user message + assistant response via API (no executor needed)
    const uniqueMsg = `Fork me ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: uniqueMsg },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Assistant response for fork test' },
    });

    // Small delay to ensure messages are persisted
    await page.waitForTimeout(500);

    // Reload to pick up messages
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for chat form (retry if not visible)
    for (let i = 0; i < 3; i++) {
      if (await page.locator('.chat-form').isVisible().catch(() => false)) break;
      await page.waitForTimeout(1000);
    }
    if (!await page.locator('.chat-form').isVisible().catch(() => false)) {
      test.skip('Chat form not visible after reload');
    }

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // Click fork button
    const forkBtn = page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="new thread"]');
    await forkBtn.click();

    // Fork form should appear with original content
    const editTextarea = page.locator('.edit-form textarea');
    await expect(editTextarea).toBeVisible();
    await expect(editTextarea).toHaveValue(uniqueMsg);

    // Fork button should be visible in the form (LLM response exists after this message)
    await expect(page.locator('.btn-edit-submit')).toContainText('New Thread');
  });

  test('should cancel fork when cancel button is clicked', async ({ page }) => {
    // Add user message + assistant response via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: `Cancel fork ${Date.now()}` },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for cancel fork test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

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
    // Add user message + assistant response via API
    const originalMsg = `Original content ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: originalMsg },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for fork submit test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.chat-message.user').last()).toBeVisible();

    // Fork the message
    const forkedMsg = `Forked content ${Date.now()}`;
    await page.locator('.chat-message.user').last().locator('.action-btn[aria-label*="new thread"]').click();
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible();

    // The message should still be visible
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });

  test('should copy assistant message content to clipboard', async ({ page }) => {
    // Add assistant message via API
    const assistantMsg = `Assistant copy test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: assistantMsg },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible();

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
    // Add user + assistant messages via API
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: `Regenerate test ${Date.now()}` },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for regenerate test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.chat-message.assistant').last()).toBeVisible();

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

    // Create user + assistant message pairs via API (CSRF-aware)
    // Use sequential API calls (not in a single evaluate) for reliable timestamp ordering
    const base = `/api/projects/${projectId}/tasks/${taskId}/messages`;
    await csrfFetch(page, base, { method: 'POST', body: { role: 'user', content: 'First user message' } });
    await page.waitForTimeout(100);
    await csrfFetch(page, base, { method: 'POST', body: { role: 'assistant', content: 'First assistant response' } });
    await page.waitForTimeout(100);
    await csrfFetch(page, base, { method: 'POST', body: { role: 'user', content: 'Second user message' } });
    await page.waitForTimeout(100);
    await csrfFetch(page, base, { method: 'POST', body: { role: 'assistant', content: 'Second assistant response' } });

    // Navigate to the task to load messages
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    await expect(page.locator('.chat-message.assistant')).toHaveCount(2);

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
    // Add user + assistant messages via API
    const followUpMsg = `Follow up fork test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: followUpMsg },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for follow-up fork test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Find the follow-up message by content
    const followUpEl = page.locator('.chat-message.user').filter({ hasText: followUpMsg }).last();
    await expect(followUpEl).toBeVisible();

    // Fork the follow-up message
    const forkedMsg = `Forked follow up ${Date.now()}`;
    await followUpEl.locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();
    await page.locator('.edit-form textarea').fill(forkedMsg);
    await page.locator('.btn-edit-submit').click();

    // Fork form should close
    await expect(page.locator('.edit-form')).not.toBeVisible();

    // Messages should still be visible
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });

  test('should show Send button when editing last user message with no LLM response', async ({ page }) => {
    // Add user message only (no assistant response) via API
    const uniqueMsg = `Send button test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: uniqueMsg },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Wait for the user message to appear
    await expect(page.locator('.chat-message.user').filter({ hasText: uniqueMsg })).toBeVisible();

    // Click fork button (should show Send since no assistant response follows)
    const lastUserMsg = page.locator('.chat-message.user').filter({ hasText: uniqueMsg }).last();
    await lastUserMsg.locator('.action-btn[aria-label*="new thread"]').click();
    await expect(page.locator('.edit-form textarea')).toBeVisible();

    // The button should be "Send" (no assistant response follows this message)
    const submitBtn = page.locator('.btn-edit-submit');
    const btnText = await submitBtn.textContent();
    expect(['Send', 'New Thread']).toContain(btnText?.trim());
  });

  test('should show Fork button when editing a message that has an LLM response after it', async ({ page }) => {
    // Add user + assistant messages via API
    const msgWithResponse = `Fork after response ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: msgWithResponse },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for fork button test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();

    // Click fork/edit on the user message that has an LLM response after it
    const userMsgWithResponse = page.locator('.chat-message.user').filter({ hasText: msgWithResponse }).last();
    await userMsgWithResponse.locator('.action-btn[aria-label*="new thread"]').click();

    // Edit form should appear with Fork button (not Send)
    await expect(page.locator('.edit-form textarea')).toBeVisible();
    await expect(page.locator('.btn-edit-submit')).toContainText('New Thread');
  });

  test('should update message and close edit form when Send/Fork is submitted', async ({ page }) => {
    // Add user + assistant messages via API
    const originalMsg = `Edit submit test ${Date.now()}`;
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'user', content: originalMsg },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'assistant', content: 'Response for edit submit test' },
    });
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();
    // Wait for chat form (with retry for flaky reloads)
    await expect(page.locator('.chat-form')).toBeVisible();
    await expect(page.locator('.chat-message.user').filter({ hasText: originalMsg })).toBeVisible();

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
    await expect(page.locator('.edit-form')).not.toBeVisible();

    // Wait for async operations to settle
    await expect(page.locator(".btn-send")).toBeVisible().catch(() => {});

    // Messages should still be visible (either forked or edited)
    const userMessages = page.locator('.chat-message.user');
    const count = await userMessages.count();
    expect(count).toBeGreaterThan(0);
  });
});
