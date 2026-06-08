import { test, expect } from '@playwright/test';
import http from 'node:http';
import { getProjectId, createTaskViaApi } from './helpers';

async function waitForSettle(timeout = 300): Promise<void> {
  await new Promise(r => setTimeout(r, timeout));
}

function apiPutConfig(body: Record<string, any>): Promise<any> {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const req = http.request('http://localhost:3000/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) },
    }, (res) => {
      let body = '';
      res.on('data', chunk => body += chunk);
      res.on('end', () => resolve(JSON.parse(body)));
    });
    req.on('error', reject);
    req.write(data);
    req.end();
  });
}

function apiGetConfig(): Promise<any> {
  return new Promise((resolve, reject) => {
    http.get('http://localhost:3000/api/config', (res) => {
      let data = '';
      res.on('data', chunk => data += chunk);
      res.on('end', () => resolve(JSON.parse(data)));
    }).on('error', reject);
  });
}

test.describe.serial('Settings Redesign', () => {
  test.beforeEach(async ({ page }) => {
    // Close any open modal
    await page.keyboard.press('Escape').catch(() => {});
    await waitForSettle(200);

    // Reset to known state with a provider
    await apiPutConfig({
      providers: [
        {
          name: 'test-localhost-9999',
          display_name: 'Test Server',
          url: 'http://localhost:9999/v1',
          models: ['model-a', 'model-b'],
          concurrency: 2,
          default_model: 'model-a',
        },
      ],
      default_provider: 'test-localhost-9999',
      workspace_dir: './worktrees',
      default_auto_commit: false,
      default_plan_mode: false,
      default_max_failed_turns: 3,
    });
    await waitForSettle(500);
    // Verify config propagated
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(1);
      expect(config.providers[0].name).toBe('test-localhost-9999');
    }).toPass({ timeout: 5000 });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should open settings modal and show provider card', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Settings header is visible
    await expect(page.getByRole('heading', { name: 'Settings' })).toBeVisible();

    // Provider card shows display name
    const card = page.locator('.provider-card').first();
    await expect(card.locator('.provider-display-name')).toContainText('Test Server');

    // Expand card if collapsed (cards with default_model start collapsed)
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await waitForSettle(300);
    }

    // URL is visible in the card header
    await expect(card.locator('.url-input')).toHaveValue('http://localhost:9999/v1');
  });

  test('should show model count badge on collapsed card', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();

    // Model count badge is visible
    const badge = card.locator('.model-count-badge');
    await expect(badge).toBeVisible();
    await expect(badge).toContainText('2 models');
  });

  test('should expand/collapse provider card details', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const details = page.locator('.provider-card .provider-details').first();
    const expandBtn = page.locator('.provider-card .btn-expand').first();

    // Check initial state (collapsed if default_model set, expanded otherwise)
    const initiallyExpanded = await details.isVisible().catch(() => false);

    // Click to toggle — should flip state
    await expandBtn.click();
    await waitForSettle(300);
    if (initiallyExpanded) {
      await expect(details).not.toBeVisible();
    } else {
      await expect(details).toBeVisible();
    }

    // Click again to toggle back
    await expandBtn.click();
    await waitForSettle(300);
    if (initiallyExpanded) {
      await expect(details).toBeVisible();
    } else {
      await expect(details).not.toBeVisible();
    }
  });

  test('should show Default badge on default model', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    // Expand card if collapsed
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await waitForSettle(300);
    }

    // Default model button shows "Default" text
    const defaultBtn = card.locator('.btn-default.active');
    await expect(defaultBtn).toBeVisible();
    await expect(defaultBtn).toContainText('Default');
  });

  test('should toggle default model selection', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    // Expand card if collapsed
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await waitForSettle(300);
    }

    // model-a is default, model-b is not
    const defaultBtns = card.locator('.btn-default');
    await expect(defaultBtns.first()).toHaveText('Default');
    await expect(defaultBtns.nth(1)).toHaveText('Set default');

    // Click to set model-b as default
    await defaultBtns.nth(1).click();
    await waitForSettle(200);

    // Now model-b should be "Default" and model-a should be "Set default"
    await expect(defaultBtns.first()).toHaveText('Set default');
    await expect(defaultBtns.nth(1)).toHaveText('Default');
  });

  test('should show Active indicator on default provider', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();

    // Active dot and badge should be visible
    await expect(card.locator('.active-dot')).toBeVisible();
    await expect(card.locator('.active-badge')).toBeVisible();
    await expect(card.locator('.active-badge')).toContainText('Active');
  });

  test('should show Test Connection button', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();

    // Expand if collapsed
    if (!await card.locator('.provider-details').isVisible()) {
      await card.locator('.provider-card-header').click();
    }

    // Test Connection button exists
    const testBtn = card.locator('.btn-test');
    await expect(testBtn).toBeVisible();
    await expect(testBtn).toContainText('Test Connection');
  });

  test('should show Task Defaults section with toggle switches', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Task Defaults section header
    await expect(page.getByText('Task Defaults')).toBeVisible();

    // Toggle switches exist
    await expect(page.getByText('Auto-commit')).toBeVisible();
    await expect(page.getByText('Plan mode')).toBeVisible();
    await expect(page.getByText('Max failed turns')).toBeVisible();

    // Toggle switches are present
    const toggles = page.locator('.toggle-switch');
    await expect(toggles).toHaveCount(2);

    // Max failed turns input
    const maxFailedInput = page.locator('#default-max-failed');
    await expect(maxFailedInput).toBeVisible();
    await expect(maxFailedInput).toHaveValue('3');
  });

  test('should toggle Auto-commit setting', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Auto-commit toggle starts inactive
    const toggle = page.locator('.toggle-switch').first();
    expect(await toggle.getAttribute('class')).not.toContain('active');

    // Click to toggle
    await toggle.click();
    await waitForSettle(200);
    expect(await toggle.getAttribute('class')).toContain('active');
  });

  test('should save task defaults to config', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Toggle Auto-commit on
    const toggle = page.locator('.toggle-switch').first();
    await toggle.click();
    await waitForSettle(200);

    // Change max failed turns
    await page.locator('#default-max-failed').fill('5');
    await waitForSettle(200);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(1000);

    // Verify config was saved
    const config = await apiGetConfig();
    expect(config.default_auto_commit).toBe(true);
    expect(config.default_max_failed_turns).toBe(5);
  });

  test('should show sticky footer while scrolling', async ({ page }) => {
    // Add many providers to force scrolling
    const manyProviders = Array.from({ length: 8 }, (_, i) => ({
      name: `provider-${i}`,
      display_name: `Provider ${i}`,
      url: `http://provider${i}.local/v1`,
      models: [`model-${i}`],
      concurrency: 1,
    }));
    await apiPutConfig({ providers: manyProviders, default_provider: '' });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Footer should be visible
    const footer = page.locator('.modal-footer');
    await expect(footer).toBeVisible();
    await expect(footer.locator('.btn-primary')).toBeVisible();
    await expect(footer.locator('.btn-primary')).toContainText('Save');
  });

  test('should show help tooltip for Concurrency', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();

    // Expand if collapsed
    if (!await card.locator('.provider-details').isVisible()) {
      await card.locator('.provider-card-header').click();
    }

    // Help icon exists next to Concurrency
    const helpIcon = card.locator('.help-icon');
    await expect(helpIcon).toBeVisible();
    expect(await helpIcon.getAttribute('title')).toContain('simultaneous');
  });

  test('should show empty state when no providers', async ({ page }) => {
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Empty state message
    const empty = page.locator('.providers-empty');
    await expect(empty).toBeVisible();
    await expect(empty).toContainText('No providers configured');
    await expect(empty).toContainText('OpenAI-compatible');
  });

  test('should add new provider and show it expanded', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const initialCount = await page.locator('.provider-card').count();

    // Add new provider
    await page.getByText('+ Add Provider').click();
    await waitForSettle(300);

    // Should have one more card
    await expect(page.locator('.provider-card')).toHaveCount(initialCount + 1);

    // New card should be expanded
    const newCard = page.locator('.provider-card').last();
    await expect(newCard.locator('.provider-details')).toBeVisible();
  });

  test('removing default model should clear default_model', async ({ page }) => {
    // Set up via API: provider with model-a as default
    await apiPutConfig({
      providers: [
        {
          name: 'remove-test',
          display_name: 'Remove Test',
          url: 'http://example.com/v1',
          models: ['model-a', 'model-b'],
          concurrency: 1,
          default_model: 'model-a',
        },
      ],
      default_provider: '',
    });
    await waitForSettle(200);

    // Reload page to pick up new config
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    await card.locator('.provider-card-header').click();
    await waitForSettle(500);

    // Verify default model badge exists
    await expect(card.locator('.btn-default.active')).toBeVisible({ timeout: 5000 });

    // Remove model-a (first model row)
    await card.locator('.btn-remove-model').first().click();
    await waitForSettle(500);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify default_model is cleared
    const config = await apiGetConfig();
    const provider = config.providers.find((p: any) => p.url === 'http://example.com/v1');
    expect(provider).toBeDefined();
    expect(provider.default_model).toBe('');
  });

  test('"Fetch Models" should clear existing models before populating new ones', async ({ page }) => {
    // Setup: provider with 2 existing models
    await apiPutConfig({
      providers: [
        { name: 'fetch-test', display_name: 'Fetch Test', url: 'http://example.com/v1', models: ['old-model-1', 'old-model-2'] },
      ],
      default_provider: '',
    });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Verify existing models are present
    const card = page.locator('.provider-card').first();
    await expect(card.locator('.model-row')).toHaveCount(2);

    // Mock the fetch-models API to return a different set of models
    await page.route('**/api/config/fetch-models', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ models: ['new-model-a', 'new-model-b', 'new-model-c'] }),
      });
    });

    // Click "Fetch Models"
    await card.locator('.btn-fetch').click();
    await waitForSettle(500);

    // Should now show exactly 3 new models (not 5 = 2 old + 3 new)
    await expect(card.locator('.model-row')).toHaveCount(3);

    // Fetched models are read-only (rendered as spans, not inputs) but still removable
    await expect(card.locator('.model-row input')).toHaveCount(0);
    const modelLabels = card.locator('.model-name-label');
    const values: string[] = [];
    for (let i = 0; i < 3; i++) {
      values.push(await modelLabels.nth(i).textContent());
    }
    expect(values).toContain('new-model-a');
    expect(values).toContain('new-model-b');
    expect(values).toContain('new-model-c');
    expect(values).not.toContain('old-model-1');
    expect(values).not.toContain('old-model-2');
  });

  test('model selector in chat should update after saving new providers in settings', async ({ page }) => {
    // Start with no providers
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `model-update-test-${Date.now()}`);

    // Navigate to task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await waitForSettle(2000);

    // Click "Start Task" if visible
    const startBtn = page.locator('.btn-start-task');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click({ timeout: 5000 }).catch(() => {});
      await waitForSettle(1000);
    }
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Model selector should NOT be visible (no providers configured)
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).not.toBeVisible();

    // Open settings and add a provider
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    const card = page.locator('.provider-card').last();
    await card.locator('input').first().fill('http://newprovider.local:9999/v1');
    await waitForSettle(200);
    await card.locator('.btn-add-model').click();
    await card.locator('.model-row input').first().fill('new-model-x');
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(1000);

    // Navigate back to the task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await waitForSettle(3000);

    const startBtn2 = page.locator('.btn-start-task');
    if (await startBtn2.isVisible().catch(() => false)) {
      await startBtn2.click({ timeout: 5000 }).catch(() => {});
      await waitForSettle(1000);
    }
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Model selector should now be visible with the new provider's model
    await expect(modelTrigger).toBeVisible({ timeout: 15000 });
    await modelTrigger.click();
    await waitForSettle();
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    await expect(page.locator('.portal-model-dropdown .model-option')).toContainText('new-model-x');
  });
});
