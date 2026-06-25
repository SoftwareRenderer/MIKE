import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig } from './helpers';

test.describe.serial('Default Plan Mode', () => {
  test.beforeEach(async ({ page }) => {
    // Close any open modal
    await page.keyboard.press('Escape').catch(() => {});
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

    // Reset config with plan mode default enabled
    await apiPutConfig(page, {
      providers: [
        {
          name: 'test-localhost-9999',
          display_name: 'Test Server',
          url: 'http://localhost:9999/v1',
          models: ['model-a'],
          concurrency: 2,
          default_model: 'model-a',
        },
      ],
      default_provider: 'test-localhost-9999',
      workspace_dir: './.worktrees',
      default_plan_mode: true,
    });
    await waitForConfig(page, c => c.default_plan_mode === true);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should pre-check plan mode in create task modal when default_plan_mode is enabled', async ({ page }) => {
    // Click "+ Add Task" button in the backlog column
    await page.locator('.add-task-btn').click();
    // Wait for the create task modal to appear
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // The plan mode checkbox should be checked
    const planModeCheckbox = page.locator('input[type="checkbox"]').first();
    await expect(planModeCheckbox).toBeChecked();
  });

  test('should NOT pre-check plan mode when default_plan_mode is disabled', async ({ page }) => {
    // Disable default plan mode
    await apiPutConfig(page, {
      providers: [
        {
          name: 'test-localhost-9999',
          display_name: 'Test Server',
          url: 'http://localhost:9999/v1',
          models: ['model-a'],
          concurrency: 2,
          default_model: 'model-a',
        },
      ],
      default_provider: 'test-localhost-9999',
      workspace_dir: './.worktrees',
      default_plan_mode: false,
    });
    await waitForConfig(page, c => c.default_plan_mode === false);

    // Reload page to pick up new config
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Click "+ Add Task" button
    await page.locator('.add-task-btn').click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // The plan mode checkbox should NOT be checked
    const planModeCheckbox = page.locator('input[type="checkbox"]').first();
    await expect(planModeCheckbox).not.toBeChecked();
  });
});
