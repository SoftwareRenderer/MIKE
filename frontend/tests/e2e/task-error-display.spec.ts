import { test, expect } from '@playwright/test';
import {waitForChatForm, navigateToTask, createTaskViaApi, getProjectId, csrfFetch} from './helpers';

test.describe.serial('Task Error Display', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item');

    // Get project ID
    projectId = await getProjectId(page);
  });

  test('should display error messages as distinct styled entries', async ({ page }) => {
    // Create a task (CSRF-aware)
    taskId = await createTaskViaApi(page, projectId, 'Test error display');

    // Navigate to the task via URL
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();
    if (!await waitForChatForm(page)) test.skip('Chat form not visible');

    // Create an error message via the API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[connection_failed] Simulated connection error' },
    });

    // Reload to pick up the new message from the backend
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Error entry should be visible with distinct styling
    const errorEntry = page.locator('.error-entry');
    await expect(errorEntry).toBeVisible();

    // Error card should have warning icon and error styling
    const errorCard = page.locator('.error-card');
    await expect(errorCard).toBeVisible();

    // Error code should be displayed
    const errorCode = page.locator('.error-code');
    await expect(errorCode).toBeVisible();
    await expect(errorCode).toContainText('connection_failed');

    // Error message should be displayed
    const errorMessage = page.locator('.error-message');
    await expect(errorMessage).toBeVisible();
    await expect(errorMessage).toContainText('Simulated connection error');
  });

  test('should remove error messages when user sends next message', async ({ page }) => {
    // Create a task (CSRF-aware)
    taskId = await createTaskViaApi(page, projectId, 'Test error cleared by user message');

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for chat form OR start button (executor may not start in test env)
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible();

    // If start button is still visible, click it
    if (await page.locator('.btn-start-task').isVisible().catch(() => false)) {
      await page.click('.btn-start-task');
    }

    // Wait for chat form
    const chatFormVisible = await page.locator('.chat-form').isVisible().catch(() => false);
    if (!chatFormVisible) {
      test.skip('Chat form not available (executor did not start in test env)');
    }

    // Create an error message via the API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[max_failures] Max consecutive failed turns reached' },
    });

    // Reload to pick up the error message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Verify error is visible
    await expect(page.locator('.error-entry')).toBeVisible();

    // Send a user message — this should clear error entries from the UI
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Hello after error');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('');

    // Error entry should be removed from the UI
    await expect(page.locator('.error-entry')).not.toBeVisible();

    // User messages should be visible (task prompt + our message)
    await expect(page.locator('.chat-message.user').first()).toBeVisible();
  });

  test('should persist error messages across page reload', async ({ page }) => {
    // Create a task
    taskId = await createTaskViaApi(page, projectId, 'Test error persists across reload');

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for chat form OR start button (executor may not start in test env)
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible();
    if (await page.locator('.btn-start-task').isVisible().catch(() => false)) {
      await page.click('.btn-start-task');
    }
    const chatFormVisible = await page.locator('.chat-form').isVisible().catch(() => false);
    if (!chatFormVisible) {
      test.skip('Chat form not available (executor did not start in test env)');
    }

    // Create an error message via the API (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[stopped] Task stopped by user' },
    });

    // Reload to pick up the error message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Verify error is visible after first reload
    await expect(page.locator('.error-entry')).toBeVisible();
    await expect(page.locator('.error-code')).toContainText('stopped');

    // Reload again — error should still be there
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Error should still be visible
    await expect(page.locator('.error-entry')).toBeVisible();
    await expect(page.locator('.error-code')).toContainText('stopped');
  });

  test('should show multiple error messages as separate entries', async ({ page }) => {
    test.setTimeout(30000);
    // Create a task
    taskId = await createTaskViaApi(page, projectId, 'Test multiple errors');

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Create multiple error messages via the API (sequential for ordering, CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[connection_failed] First error' },
    });
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[max_failures] Second error' },
    });

    // Reload to pick up the error messages
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Both error entries should be visible
    const errorEntries = page.locator('.error-entry');
    await expect(errorEntries).toHaveCount(2);

    // First error should have connection_failed code
    await expect(errorEntries.nth(0).locator('.error-code')).toContainText('connection_failed');

    // Second error should have max_failures code
    await expect(errorEntries.nth(1).locator('.error-code')).toContainText('max_failures');
  });

  test('should render error entry with warning icon and styled card', async ({ page }) => {
    // Create a task
    taskId = await createTaskViaApi(page, projectId, 'Test error entry rendering');

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Wait for chat form OR start button (executor may not start in test env)
    await expect(page.locator('.chat-form, .btn-start-task')).toBeVisible();
    if (await page.locator('.btn-start-task').isVisible().catch(() => false)) {
      await page.click('.btn-start-task');
    }
    const chatFormVisible = await page.locator('.chat-form').isVisible().catch(() => false);
    if (!chatFormVisible) {
      test.skip('Chat form not available (executor did not start in test env)');
    }

    // Create an error message (CSRF-aware)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: { role: 'error', content: '[panic] Internal server error' },
    });

    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible();

    // Verify error entry renders with correct structure
    await expect(page.locator('.error-entry')).toBeVisible();
    await expect(page.locator('.error-card')).toBeVisible();
    await expect(page.locator('.error-code')).toContainText('panic');
    await expect(page.locator('.error-message')).toContainText('Internal server error');

    // Verify the warning icon (SVG) is present in the error header
    const errorHeader = page.locator('.error-header');
    await expect(errorHeader).toBeVisible();
    // The SVG warning icon should be present (use .first() to avoid strict mode violation)
    await expect(errorHeader.locator('svg').first()).toBeVisible();
  });
});
