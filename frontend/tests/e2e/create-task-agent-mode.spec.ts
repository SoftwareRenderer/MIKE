import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.serial('Agent provider hides operating mode', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  // Helper to add agent provider to config
  async function addAgentProvider(page: any) {
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const csrf = await page.evaluate(async () => {
      const res = await fetch('/api/auth/session');
      const data = await res.json();
      return data.csrf_token || '';
    });

    await page.evaluate(async ({ csrfToken, config }) => {
      const providers = [...(config.providers || [])];
      const hasAgent = providers.some((p: any) => p.url?.startsWith('agent://'));
      if (!hasAgent) {
        providers.push({
          name: 'pi',
          display_name: 'Pi Coding Agent',
          kind: 'agent',
          url: 'agent://pi',
          models: [],
          concurrency: 1,
        });
      }
      fetch('/api/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
        credentials: 'include',
        body: JSON.stringify({ ...config, providers }),
      });
    }, { csrfToken: csrf, config });

    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });
  }

  test('should hide Operating Mode when agent is selected in Create Task modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    await addAgentProvider(page);

    // Open the Create Task modal
    await page.locator('.add-task-btn').first().click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Verify Operating Mode is visible with default (LLM) provider
    await expect(page.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Select the agent provider
    await page.locator('.provider-trigger').click();
    await page.locator('.provider-option-agent').click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is now hidden
    await expect(page.locator('.config-label:has-text("Operating mode")')).not.toBeVisible();
    await expect(page.locator('.mode-btn')).not.toBeVisible();

    // Switch back to LLM provider
    await page.locator('.provider-trigger').click();
    const llmOptions = page.locator('.provider-option:not(.provider-option-agent)');
    await expect(llmOptions).toHaveCount(1);
    await llmOptions.first().click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is visible again
    await expect(page.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.mode-btn')).toBeVisible();

    // Close modal
    await page.locator('.modal .btn-secondary').click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
  });

  test('should hide Operating Mode when agent is selected in Edit Task modal', async ({ page }) => {
    const projectId = await getProjectId(page);
    await addAgentProvider(page);

    // Get an existing task
    const cards = page.locator('.card');
    await expect(cards.first()).toBeVisible({ timeout: 10000 });
    const taskId = await cards.first().getAttribute('data-task-id') || '';
    expect(taskId).not.toBe('');

    // Navigate to the task to see the card with edit button
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for the selected card to be visible
    await expect(page.locator('.card.selected')).toBeVisible({ timeout: 5000 });

    // Open the edit modal
    const editButton = page.locator('.card.selected').locator('button[aria-label="Edit task"]');
    await expect(editButton).toBeVisible();
    await editButton.click();

    const modal = page.locator('.modal[aria-labelledby="modal-title"]');
    await expect(modal).toBeVisible({ timeout: 5000 });

    // Verify Operating Mode is visible (task was created with LLM provider)
    await expect(modal.locator('.config-label:has-text("Operating mode")')).toBeVisible();
    await expect(modal.locator('.mode-btn')).toBeVisible();

    // Select the agent provider (scope to modal)
    await modal.locator('.provider-trigger').click();
    await page.locator('.provider-option-agent').click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is now hidden
    await expect(modal.locator('.config-label:has-text("Operating mode")')).not.toBeVisible();
    await expect(modal.locator('.mode-btn')).not.toBeVisible();

    // Switch back to LLM provider
    await modal.locator('.provider-trigger').click();
    const llmOptions = page.locator('.provider-option:not(.provider-option-agent)');
    await expect(llmOptions).toHaveCount(1);
    await llmOptions.first().click();
    await page.waitForTimeout(300);

    // Verify Operating Mode is visible again
    await expect(modal.locator('.config-label:has-text("Operating mode")')).toBeVisible({ timeout: 5000 });
    await expect(modal.locator('.mode-btn')).toBeVisible();

    // Cancel without saving
    await page.locator('.modal .btn-secondary').click();
    await expect(modal).not.toBeVisible({ timeout: 5000 });
  });
});
