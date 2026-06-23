import { test, expect } from '@playwright/test';
import {getProjectId, navigateToTask, csrfFetch, apiGetConfig} from './helpers';

test.describe.serial('Task Chat Sticky Header', () => {
  test.use({ viewport: { width: 1280, height: 600 } });
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  /** Create a task with a long prompt to ensure the chat area is scrollable. */
  async function createScrollableTask(page: any): Promise<string> {
    const projectId = await getProjectId(page);
    const longPrompt = 'Implement a comprehensive feature that includes:\n\n' +
      '1. Create a new API endpoint for user management\n' +
      '2. Add input validation and error handling\n' +
      '3. Write unit tests for all edge cases\n' +
      '4. Update the documentation with new API references\n' +
      '5. Add integration tests for the full workflow\n' +
      '6. Implement caching for frequently accessed data\n' +
      '7. Add logging and monitoring hooks\n' +
      '8. Create migration scripts for existing data\n' +
      '9. Update the frontend to use the new endpoint\n' +
      '10. Add proper error messages and user feedback\n\n' +
      'Please ensure all changes follow the existing code style and patterns.';
    const config = await apiGetConfig(page);
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: longPrompt,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      },
    });
    return result.data.id;
  }

  test('should show task title header element', async ({ page }) => {
    const taskId = await createScrollableTask(page);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message')).toBeVisible({ timeout: 10000 });

    // Wait for title-gen to complete (title_generating becomes false)
    await page.waitForFunction((tid) => {
      return document.querySelector('.card-title-text') !== null;
    }, taskId, { timeout: 10000 });

    // Read the task title from the API (after title-gen completes)
    const taskTitle = await page.evaluate(async ({ tid }) => {
      const projects = await (await fetch('/api/projects')).json();
      const projectId = projects[0].id;
      const task = await (await fetch(`/api/projects/${projectId}/tasks/${tid}`)).json();
      return task.title;
    }, { tid: taskId });

    const header = page.locator('.chat-task-header');
    const titleEl = page.locator('.chat-task-title');
    await expect(header).toBeVisible({ timeout: 5000 });
    await expect(titleEl).toContainText(taskTitle);
    // At top: opacity should be 0
    await expect(header).toHaveCSS('opacity', '0');
  });

  test('should show header when scrolled and hide when at top', async ({ page }) => {
    const taskId = await createScrollableTask(page);
    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-message')).toBeVisible({ timeout: 10000 });

    const header = page.locator('.chat-task-header');
    const chatMessages = page.locator('.chat-messages');

    // Verify initially hidden
    await expect(header).toHaveCSS('opacity', '0');

    // Click the element to give it focus, then PageDown
    await chatMessages.click({ position: { x: 10, y: 10 } });
    await page.waitForTimeout(100);
    await page.keyboard.press('PageDown');
    await page.keyboard.press('PageDown');
    await page.keyboard.press('PageDown');
    await page.waitForTimeout(300);

    // Check scroll position changed
    const scrollTop = await chatMessages.evaluate(el => el.scrollTop);
    expect(scrollTop).toBeGreaterThan(60);

    // Header should be visible
    await expect(header).toHaveCSS('opacity', '1');

    // Scroll back to top with Home
    await chatMessages.focus();
    await page.keyboard.press('Home');
    await page.waitForTimeout(300);

    // Header should be hidden again
    await expect(header).toHaveCSS('opacity', '0');
  });
});
