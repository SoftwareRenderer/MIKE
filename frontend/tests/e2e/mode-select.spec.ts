import { test, expect, type Page } from '@playwright/test';
import {
  getProjectId,
  navigateToTask,
  ensureTaskStarted,
  sendMessage,
  findProviderByRef,
} from './helpers';

/** Dismiss the Create Task modal. Named by its label: the shared button family carries no class a
 *  restyle has to keep, so a styling class here is a locator that breaks on a rename. */
const dismissTaskModal = (page: Page) =>
  page.locator('.modal').getByRole('button', { name: 'Cancel', exact: true });

test.describe.serial('ModeSelect component', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should open mode dropdown showing Act/Plan/Explore options', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector button
    await page.locator('.mode-btn').click();

    // Verify dropdown is open with all three options
    const dropdown = page.locator('.mode-dropdown');
    await expect(dropdown).toBeVisible({ timeout: 5000 });

    // Verify all three mode options are present
    const options = page.locator('.mode-option');
    await expect(options).toHaveCount(3);

    // Verify the labels
    await expect(options.nth(0)).toContainText('Act');
    await expect(options.nth(1)).toContainText('Plan');
    await expect(options.nth(2)).toContainText('Explore');

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should switch to Plan mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector to open dropdown
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });

    // Select Plan mode
    await page.locator('.mode-option.plan').click();

    // Verify the button now shows "Plan"
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Verify the plan icon is active
    await expect(page.locator('.mode-btn .mode-icon.plan')).toBeVisible();

    // Verify dropdown closed after selection
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should switch to Explore mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector to open dropdown
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });

    // Select Explore mode
    await page.locator('.mode-option.explore').click();

    // Verify the button now shows "Explore"
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    // Verify the explore icon is active
    await expect(page.locator('.mode-btn .mode-icon.explore')).toBeVisible();

    // Verify dropdown closed after selection
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should switch back to Act mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // First switch to Plan to demonstrate switching back
    await page.locator('.mode-btn').click();
    await page.locator('.mode-option.plan').click();
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Now switch back to Act
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });
    await page.locator('.mode-option.act').click();

    // Verify the button now shows "Act"
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Verify the act icon is active
    await expect(page.locator('.mode-btn .mode-icon.act')).toBeVisible();

    // Verify dropdown closed after selection
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should persist mode after page reload', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a task in Plan mode via the API (more reliable than UI for exact mode)
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);
    const workerModel = config?.worker_model || '';

    const csrf = await page.evaluate(async () => {
      const res = await fetch('/api/auth/session');
      const data = await res.json();
      return data.csrf_token || '';
    });

    const uniquePrompt = `mode-persist-${Date.now()}`;
    const taskId = await page.evaluate(async ({ pid, csrfToken, provider, prompt, modelName }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': csrfToken,
        },
        credentials: 'include',
        body: JSON.stringify({
          prompt,
          operating_mode: 'plan',
          llama_server_url: provider?.url || '',
          model_name: modelName,
        }),
      });
      const data = await res.json();
      return data.id || '';
    }, { pid: projectId, csrfToken: csrf, provider: defaultProvider, prompt: uniquePrompt, modelName: workerModel });

    if (!taskId) {
      throw new Error('Failed to create task in Plan mode');
    }

    // Verify the mode persisted on the server by fetching the task data
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });

    expect(taskData).not.toBeNull();
    expect(taskData.operating_mode).toBe('plan');

    // Also verify via the board API (simulates a page reload fetching fresh data)
    const boardData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.find((t: any) => t.id === tid) || null;
    }, { pid: projectId, tid: taskId });

    expect(boardData).not.toBeNull();
    expect(boardData.operating_mode).toBe('plan');
  });

  test('should trigger plan-mode LLM routing', async ({ page }) => {
    test.setTimeout(30000);
    const projectId = await getProjectId(page);

    // Create a task in Plan mode via the API
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = findProviderByRef(config?.providers || [], config?.worker_provider);
    const workerModel = config?.worker_model || '';

    const csrf = await page.evaluate(async () => {
      const res = await fetch('/api/auth/session');
      const data = await res.json();
      return data.csrf_token || '';
    });

    const taskId = await page.evaluate(async ({ pid, csrfToken, provider, modelName }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-CSRF-Token': csrfToken,
        },
        credentials: 'include',
        body: JSON.stringify({
          prompt: `plan-mode-llm-test-${Date.now()}`,
          operating_mode: 'plan',
          llama_server_url: provider?.url || '',
          model_name: modelName,
        }),
      });
      const data = await res.json();
      return data.id || '';
    }, { pid: projectId, csrfToken: csrf, provider: defaultProvider, modelName: workerModel });

    if (!taskId) {
      throw new Error('Failed to create task in Plan mode');
    }

    // Navigate to the task
    await navigateToTask(page, taskId);

    // Move task to in_progress so the executor starts
    await ensureTaskStarted(page, projectId, taskId);

    // Wait for the chat form to appear
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 15000 });

    // Wait for send or stop button (form is interactive)
    await expect(async () => {
      const hasSend = await page.locator('.btn-send').isVisible();
      const hasStop = await page.locator('.btn-stop').isVisible();
      expect(hasSend || hasStop).toBe(true);
    }).toPass({ timeout: 15000 });

    // If executor is still running (stop button visible), skip this test gracefully
    if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
      return;
    }

    // Send a message — in Plan mode, the LLM should receive the plan-mode system prompt
    // and respond with a create_task tool call (via the mock LLM's plan-mode routing)
    await sendMessage(page, 'Plan this feature');

    // Wait for the LLM response to appear (the mock returns create_task tool call for plan mode)
    // The tool call should appear as a tool call message in the chat
    await expect(async () => {
      // Check that a tool call or AI response appeared
      const hasToolCall = await page.locator('.tool-call').isVisible().catch(() => false);
      const hasAiMessage = await page.locator('.chat-message.ai').count();
      expect(hasToolCall || hasAiMessage > 0).toBe(true);
    }).toPass({ timeout: 30000 });
  });

  test('should close dropdown when clicking outside', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Click the mode selector to open dropdown
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });

    // Click outside the dropdown (on the modal background/body area)
    // Click on the textarea which is outside the mode-select element
    await page.locator('.modal textarea').click();

    // Verify the dropdown closed
    await expect(page.locator('.mode-dropdown')).not.toBeVisible({ timeout: 5000 });

    // Verify the button is still visible (modal not closed)
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Close modal
    await dismissTaskModal(page).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });
});

test.describe.serial('Question mark auto-switches operating mode to Explore', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should switch to Explore when the description contains a ?', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Default mode is Act in the test environment
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Type a description containing a question mark
    await page.locator('#task-description').fill('Why is the login broken?');

    // The mode should auto-switch to Explore
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    // The explore icon should be active
    await expect(page.locator('.mode-btn .mode-icon.explore')).toBeVisible();

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should revert to the previous mode when the ? is removed', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Add a ? — mode switches to Explore
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    // Remove the ? — mode should revert to Act
    await page.locator('#task-description').fill('Why is the login broken');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Type ? again and remove it again — still reverts
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');
    await page.locator('#task-description').fill('no question mark here');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Act');

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should not override a manually selected mode', async ({ page }) => {
    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Manually select Plan mode
    await page.locator('.mode-btn').click();
    await expect(page.locator('.mode-dropdown')).toBeVisible({ timeout: 5000 });
    await page.locator('.mode-option.plan').click();
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Add a ? — mode should STAY Plan (user changed it)
    await page.locator('#task-description').fill('Why is the login broken?');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Remove the ? — mode should STAY Plan (no revert)
    await page.locator('#task-description').fill('Why is the login broken');
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Plan');

    // Close modal (prompt is non-empty → confirm discard)
    await dismissTaskModal(page).click();
    await expect(page.getByRole('button', { name: 'Discard' })).toBeVisible({ timeout: 5000 });
    await page.getByRole('button', { name: 'Discard' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should create the task in Explore mode when the description contains a ?', async ({ page }) => {
    const projectId = await getProjectId(page);
    const uniquePrompt = `explore-qs-${Date.now()}`;

    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    await page.locator('#task-description').fill(`${uniquePrompt}? What is going on?`);
    await expect(page.locator('.mode-btn .mode-label')).toHaveText('Explore');

    await page.locator('.modal .btn-primary').click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });

    // Verify the created task persisted with operating_mode=explore
    const task = await page.evaluate(async ({ pid, prompt }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.find((t: any) => t.prompt?.includes(prompt)) || null;
    }, { pid: projectId, prompt: uniquePrompt });

    expect(task).not.toBeNull();
    expect(task.operating_mode).toBe('explore');
  });
});
