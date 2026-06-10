import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';
import http from 'node:http';

async function waitForSettle(timeout = 500): Promise<void> {
  await new Promise(r => setTimeout(r, timeout));
}

function apiPutConfig(body: Record<string, any>): Promise<void> {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const req = http.request('http://localhost:3000/api/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(data) },
    }, (res) => {
      res.on('data', () => {});
      res.on('end', () => resolve());
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

// Create a task via API with a unique title and open it via URL navigation.
// If waitForModelTrigger is true, verifies config has providers before navigating
// (avoids race with in-memory override) and waits for the model selector.
// Returns the task ID.
async function createAndOpenTask(page: any, projectId: string, waitForModelTrigger = true): Promise<string> {
  const title = `task-${Date.now()}`;
  const taskId = await createTaskViaApi(page, projectId, title);
  // When waiting for model trigger, verify config has providers BEFORE navigating.
  // On first test run, the in-memory override may not have propagated yet.
  // Retry with explicit config set if needed.
  if (waitForModelTrigger) {
    let attempts = 0;
    const maxAttempts = 3;
    while (attempts < maxAttempts) {
      const cfg = await page.evaluate(async () => {
        const res = await fetch('/api/config');
        return res.json();
      });
      if (cfg.providers && cfg.providers.length > 0) break;
      attempts++;
      if (attempts < maxAttempts) {
        await apiPutConfig({
          providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
          default_provider: 'test-provider',
        });
        await waitForSettle(500);
      }
    }
  }
  // Navigate to the task URL
  await page.goto(`/?task=${taskId}`);
  await page.waitForSelector('.task-detail-page', { timeout: 10000 });
  // Wait for onMount to complete (loadProviders + loadHistory + connect)
  await waitForSettle(3000);
  // Handle "Start Task" button if task is new
  const startBtn = page.locator('.btn-start-task');
  if (await startBtn.isVisible().catch(() => false)) {
    await startBtn.click({ timeout: 5000 }).catch(() => {}); // may auto-dismiss
    await waitForSettle(1000);
  }
  // Wait for chat form
  await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
  if (waitForModelTrigger) {
    // Wait for model trigger with generous timeout (loadProviders may be slow on cold load)
    await expect(page.locator('.model-trigger')).toBeVisible({ timeout: 15000 });
  }
  return taskId;
}

test.describe.serial('Model Selector', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig({
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await waitForSettle(500);
    // Verify config propagated
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(1);
      expect(config.providers[0].name).toBe('test-provider');
    }).toPass({ timeout: 5000 });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show model selector in chat form when providers are configured', async ({ page }) => {
    const projectId = await getProjectId(page);
    await createAndOpenTask(page, projectId);
    // model-trigger visibility is already checked in createAndOpenTask
    await expect(page.locator('.model-selector')).toBeVisible();
  });

  test('should open dropdown when model trigger is clicked', async ({ page }) => {
    const projectId = await getProjectId(page);
    await createAndOpenTask(page, projectId);
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    await expect(page.locator('.portal-model-dropdown .model-option')).toHaveCount(2);
    // With single provider, no provider selector is shown (no .provider-label)
    await expect(page.locator('.provider-trigger')).not.toBeVisible();
  });

  test('should update task when a model is selected', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createAndOpenTask(page, projectId);
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await page.locator('.portal-model-dropdown .model-option').filter({ hasText: 'model-beta' }).click();
    await waitForSettle(1000);
    await expect(page.locator('.model-name')).toContainText('model-beta');
    // Wait for the API call to complete
    await waitForSettle(500);
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`${window.location.origin}/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.model_name).toBe('model-beta');
    expect(taskData.llama_server_url).toBe('http://localhost:8080/v1');
  });

  test('should not show model selector when no providers are configured', async ({ page }) => {
    await apiPutConfig({ providers: [], default_provider: '' });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createAndOpenTask(page, projectId, false); // don't wait for model trigger
    await expect(page.locator('.model-selector')).not.toBeVisible();
  });

  test('should show provider selector with multiple providers', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'provider-a', display_name: 'Provider A', url: 'http://a.example/v1', models: ['model-a1', 'model-a2'], default_model: 'model-a1' },
        { name: 'provider-b', display_name: 'Provider B', url: 'http://b.example/v1', models: ['model-b1'], default_model: 'model-b1' },
      ],
      default_provider: 'provider-a',
    });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createAndOpenTask(page, projectId);
    // Provider selector should be visible (2+ providers)
    await expect(page.locator('.provider-trigger')).toBeVisible();
    await expect(page.locator('.provider-trigger')).toContainText('Provider A');
    // Open provider dropdown
    await page.locator('.provider-trigger').click();
    await waitForSettle();
    await expect(page.locator('.portal-provider-dropdown')).toBeVisible();
    await expect(page.locator('.portal-provider-dropdown .provider-option')).toHaveCount(2);
    // Switch to Provider B
    await page.locator('.portal-provider-dropdown .provider-option').filter({ hasText: 'Provider B' }).click();
    await waitForSettle(500);
    // Model should switch to Provider B's default
    await expect(page.locator('.model-trigger')).toContainText('model-b1');
    // Open model dropdown - should only show Provider B's models
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await expect(page.locator('.portal-model-dropdown .model-option')).toHaveCount(1);
    await expect(page.locator('.portal-model-dropdown .model-option')).toContainText('model-b1');
  });
});

test.describe.serial('Dropdown flip positioning', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig({
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(1);
      expect(config.providers[0].name).toBe('test-provider');
    }).toPass({ timeout: 5000 });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should flip dropdown upward when trigger is near the bottom of the viewport', async ({ page }) => {
    const projectId = await getProjectId(page);
    // Use a small viewport so the model trigger is near the bottom
    await page.setViewportSize({ width: 1280, height: 600 });
    await createAndOpenTask(page, projectId);
    // Scroll to ensure trigger is visible, then get position via evaluate
    const triggerRect = await page.evaluate(() => {
      const el = document.querySelector('.model-trigger');
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { top: r.top, bottom: r.bottom, height: r.height };
    });
    expect(triggerRect).not.toBeNull();
    const triggerTop = triggerRect!.top;
    // Open the model dropdown
    await page.locator('.model-trigger').click();
    await waitForSettle(500);
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    // Get the dropdown position via evaluate
    const dropdownRect = await page.evaluate(() => {
      const el = document.querySelector('.portal-model-dropdown');
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { top: r.top, bottom: r.bottom, height: r.height };
    });
    expect(dropdownRect).not.toBeNull();
    const dropdownBottom = dropdownRect!.bottom;
    // When flipped, the dropdown should be positioned above the trigger
    // i.e., dropdown bottom should be at or above the trigger's top
    expect(dropdownBottom).toBeLessThanOrEqual(triggerTop + 10); // 10px tolerance
  });
});

test.describe.serial('Config Providers', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should add a provider via settings modal', async ({ page }) => {
    // Clear existing providers so the new one is the only card
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(500);
    // Reload page to pick up config change
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    // Now there's only one provider card (the new one)
    const card = page.locator('.provider-card').last();
    // Use a unique URL to avoid conflicts with parallel tests
    const uniqueUrl = `http://test-provider-${Date.now()}.local/v1`;
    // Only URL input remains — name and display_name are auto-generated from URL
    await card.locator('input').first().fill(uniqueUrl);
    await waitForSettle(200); // allow auto-generation to run
    await page.getByRole('button', { name: 'Save' }).click();
    // Poll for the provider to appear (parallel tests may modify config)
    await expect(async () => {
      const config = await apiGetConfig();
      const myProvider = config.providers.find((p: any) => p.url === uniqueUrl);
      expect(myProvider).toBeDefined();
      expect(myProvider.name).toMatch(/test-provider/);
      expect(myProvider.display_name).toMatch(/test-provider/i);
      expect(myProvider.url).toBe(uniqueUrl);
    }).toPass({ timeout: 10000 });
  });

  test('should have Fetch Models button in provider card', async ({ page }) => {
    // Clear existing providers so the new one is the only card
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    const card = page.locator('.provider-card').last();
    await card.locator('input').first().fill('http://example.com/v1');
    const fetchBtn = card.locator('.btn-fetch');
    await expect(fetchBtn).toBeVisible();
    await expect(fetchBtn).toBeEnabled();
    await expect(fetchBtn).toContainText('Fetch Models');
  });
});

test.describe.serial('CreateTaskModal Provider Selector', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await waitForSettle(200);
    await apiPutConfig({
      providers: [
        {
          name: 'provider-alpha',
          display_name: 'Alpha Server',
          url: 'http://alpha.local:8080/v1',
          models: ['alpha-model-1', 'alpha-model-2'],
          concurrency: 1,
          default_model: 'alpha-model-1',
        },
        {
          name: 'provider-beta',
          display_name: 'Beta Server',
          url: 'http://beta.local:9090/v1',
          models: ['beta-model-1', 'beta-model-2', 'beta-model-3'],
          concurrency: 1,
          default_model: 'beta-model-1',
        },
      ],
      default_provider: '',
      workspace_dir: './.worktrees',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(2);
      expect(config.providers[0].name).toBe('provider-alpha');
    }).toPass({ timeout: 5000 });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('create task modal shows provider selector with multiple providers', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Provider selector should be visible (more than 1 provider)
    const providerTrigger = page.locator('.provider-trigger');
    await expect(providerTrigger).toBeVisible();
    await expect(providerTrigger).toContainText('Alpha Server');

    // Model selector should show the first provider's default model
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).toBeVisible();
    await expect(modelTrigger).toContainText('alpha-model-1');
  });

  test('switching provider updates model list in create task modal', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Click provider dropdown
    await page.locator('.provider-trigger').click();
    await page.waitForSelector('.portal-provider-dropdown', { timeout: 5000 });

    // Should show both providers
    const providerOptions = page.locator('.portal-provider-dropdown .provider-option');
    await expect(providerOptions).toHaveCount(2);

    // Select Beta Server
    await providerOptions.nth(1).click();
    await waitForSettle(300);

    // Model selector should now show beta-model-1 (Beta's default)
    await expect(page.locator('.model-trigger')).toContainText('beta-model-1');
  });

  test('single provider hides provider selector in create task modal', async ({ page }) => {
    await apiPutConfig({
      providers: [
        {
          name: 'only-provider',
          display_name: 'Only Server',
          url: 'http://only.local:7070/v1',
          models: ['only-model'],
          concurrency: 1,
        },
      ],
      default_provider: '',
    });
    await waitForSettle(200);
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Provider selector should NOT be visible (only 1 provider)
    await expect(page.locator('.provider-trigger')).not.toBeVisible();

    // Model selector should still be visible
    await expect(page.locator('.model-trigger')).toBeVisible();
  });

  // Restore config after all model-selector tests so other tests work correctly
  test.afterAll(async () => {
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
    });
    await waitForSettle(500);
  });
});
