import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi, sendViaWebSocket, waitForChatForm } from './helpers';

test.describe('Chat send button with no model selected', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should disable send button when no model is selected', async ({ page }) => {
    // Intercept /api/config to return providers with empty model lists
    // and no fallback llama_server_url/model_name, so no model is auto-selected
    await page.route('/api/config', async (route) => {
      const config = {
        llama_server_url: '',
        model_name: '',

        workspace_dir: '/home/mwren/projects/kanban/.worktrees',
        providers: [
          {
            name: 'test-provider',
            display_name: 'Test Provider',
            url: 'http://localhost:9999/v1',
            models: [],
            concurrency: 1,
          },
        ],
        default_provider: '',
      };
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(config),
      });
    });

    // Create a task via API
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `no-model-test-${Date.now()}`);

    // Trigger lazy-start via WebSocket so the task has messages (chat form shows)
    await sendViaWebSocket(page, projectId, taskId, 'hello');

    // Navigate to the task with URL parameter (triggers fresh load with intercepted config)
    await page.goto(`/?task=${taskId}`);
    await waitForChatForm(page);

    // Model selector should be visible (providers exist)
    const modelSelector = page.locator('.model-selector');
    await expect(modelSelector).toBeVisible({ timeout: 5000 });

    // Send button should be disabled (no model selected)
    const sendButton = page.locator('.btn-send');
    await expect(sendButton).toBeVisible();
    await expect(sendButton).toBeDisabled();

    // Typing should NOT enable the button (still no model)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('Test message');
    await expect(sendButton).toBeDisabled();
  });
});
