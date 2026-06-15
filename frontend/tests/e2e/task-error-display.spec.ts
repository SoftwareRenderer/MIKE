import { test, expect } from '@playwright/test';
import { waitForChatForm } from './helpers';

test.describe.serial('Task Error Display', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page, request }) => {
    test.setTimeout(30000);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Get project ID via Playwright request (more reliable than page.evaluate + fetch)
    const projects = await request.get('/api/projects');
    projectId = (await projects.json())[0].id;
  });

  test('should display error messages as distinct styled entries', async ({ page, request }) => {
    // Create a task
    const taskRes = await request.post(`/api/projects/${projectId}/tasks`, {
      data: { prompt: 'Test error display' },
    });
    taskId = (await taskRes.json()).id;

    // Navigate to the task via URL
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await waitForChatForm(page);

    // Create an error message via the API
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[connection_failed] Simulated connection error' },
    });

    // Reload to pick up the new message from the backend
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Error entry should be visible with distinct styling
    const errorEntry = page.locator('.error-entry');
    await expect(errorEntry).toBeVisible({ timeout: 5000 });

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

  test('should remove error messages when user sends next message', async ({ page, request }) => {
    // Create a task
    const taskRes = await request.post(`/api/projects/${projectId}/tasks`, {
      data: { prompt: 'Test error cleared by user message' },
    });
    taskId = (await taskRes.json()).id;

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await waitForChatForm(page);

    // Create an error message via the API
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[max_failures] Max consecutive failed turns reached' },
    });

    // Reload to pick up the error message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Verify error is visible
    await expect(page.locator('.error-entry')).toBeVisible({ timeout: 5000 });

    // Send a user message — this should clear error entries from the UI
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Hello after error');
    await chatTextarea.press('Enter');

    // Wait for textarea to be cleared (message sent)
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Error entry should be removed from the UI
    await expect(page.locator('.error-entry')).not.toBeVisible();

    // User messages should be visible (task prompt + our message)
    await expect(page.locator('.chat-message.user').first()).toBeVisible({ timeout: 5000 });
  });

  test('should persist error messages across page reload', async ({ page, request }) => {
    // Create a task
    const taskRes = await request.post(`/api/projects/${projectId}/tasks`, {
      data: { prompt: 'Test error persists across reload' },
    });
    taskId = (await taskRes.json()).id;

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await waitForChatForm(page);

    // Create an error message via the API
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[stopped] Task stopped by user' },
    });

    // Reload to pick up the error message
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Verify error is visible after first reload
    await expect(page.locator('.error-entry')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.error-code')).toContainText('stopped');

    // Reload again — error should still be there
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Error should still be visible
    await expect(page.locator('.error-entry')).toBeVisible();
    await expect(page.locator('.error-code')).toContainText('stopped');
  });

  test('should show multiple error messages as separate entries', async ({ page, request }) => {
    test.setTimeout(30000);
    // Create a task
    const taskRes = await request.post(`/api/projects/${projectId}/tasks`, {
      data: { prompt: 'Test multiple errors' },
    });
    taskId = (await taskRes.json()).id;

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Create multiple error messages via the API (sequential for ordering)
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[connection_failed] First error' },
    });
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[max_failures] Second error' },
    });

    // Reload to pick up the error messages
    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Both error entries should be visible
    const errorEntries = page.locator('.error-entry');
    await expect(errorEntries).toHaveCount(2, { timeout: 5000 });

    // First error should have connection_failed code
    await expect(errorEntries.nth(0).locator('.error-code')).toContainText('connection_failed');

    // Second error should have max_failures code
    await expect(errorEntries.nth(1).locator('.error-code')).toContainText('max_failures');
  });

  test('should render error entry with warning icon and styled card', async ({ page, request }) => {
    // Create a task
    const taskRes = await request.post(`/api/projects/${projectId}/tasks`, {
      data: { prompt: 'Test error entry rendering' },
    });
    taskId = (await taskRes.json()).id;

    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });
    await waitForChatForm(page);

    // Create an error message
    await request.post(`/api/projects/${projectId}/tasks/${taskId}/messages`, {
      data: { role: 'error', content: '[panic] Internal server error' },
    });

    await page.reload();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Verify error entry renders with correct structure
    await expect(page.locator('.error-entry')).toBeVisible({ timeout: 5000 });
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
