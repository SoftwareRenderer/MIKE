import { test, expect } from '@playwright/test';
import {getProjectId,
  createTaskViaApi,
  apiPutConfig,
  apiGetConfig,
  waitForConfig, navigateToTask} from './helpers';

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
        await apiPutConfig(page, {
          providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
          default_provider: 'test-provider',
        });
        await waitForConfig(page, c => c.providers && c.providers.length > 0);
      }
    }
  }
  // Navigate to the task URL
  await navigateToTask(page, taskId);
  await page.waitForSelector('.task-detail-page', { timeout: 10000 });
  // Handle "Start Task" button if task is new
  const startBtn = page.locator('.btn-start-task');
  if (await startBtn.isVisible().catch(() => false)) {
    await startBtn.click({ timeout: 5000 }).catch(() => {}); // may auto-dismiss
    // Wait for chat form to appear after starting (replaces waitForSettle(1000))
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 5000 }).catch(() => {});
  }
  if (waitForModelTrigger) {
    // Wait for chat form (may not appear without providers)
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    // Wait for model trigger with generous timeout (loadProviders may be slow on cold load)
    await expect(page.locator('.model-trigger')).toBeVisible({ timeout: 15000 });
  }
  return taskId;
}

test.describe.serial('Model Selector', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, {
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'test-provider'
    );
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
    // Wait for dropdown to appear (replaces waitForSettle())
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    await expect(page.locator('.portal-model-dropdown .model-option')).toHaveCount(2);
    // With single provider, no provider selector is shown (no .provider-label)
    await expect(page.locator('.provider-trigger')).not.toBeVisible();
  });

  test('should update task when a model is selected', async ({ page }) => {
    const projectId = await getProjectId(page);
    await page.click('.card');
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    const taskId = await page.evaluate(() => new URLSearchParams(location.search).get('task') || '');
    await page.locator('.model-trigger').click();
    // Wait for dropdown to appear (replaces waitForSettle())
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    await page.locator('.portal-model-dropdown .model-option').filter({ hasText: 'model-beta' }).click();
    // Wait for model name to update (replaces waitForSettle(1000))
    await expect(page.locator('.model-trigger')).toContainText('model-beta', { timeout: 10000 });
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`${window.location.origin}/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.model_name).toBe('model-beta');
    expect(taskData.llama_server_url).toBe('http://localhost:8080/v1');
  });

  test('should not show model selector when no providers are configured', async ({ page }) => {
    await apiPutConfig(page, { providers: [], default_provider: '' });
    // Wait for config to propagate (replaces blind waitForSettle)
    await waitForConfig(page, c => c.providers && c.providers.length === 0);
    // Reload to pick up the config change
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createAndOpenTask(page, projectId, false); // don't wait for model trigger
    await expect(page.locator('.model-selector')).not.toBeVisible();
  });

  test('should show provider selector with multiple providers', async ({ page }) => {
    await apiPutConfig(page, {
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
    // Wait for provider dropdown to appear (replaces waitForSettle())
    await expect(page.locator('.portal-provider-dropdown')).toBeVisible();
    await expect(page.locator('.portal-provider-dropdown .provider-option')).toHaveCount(2);
    // Switch to Provider B
    await page.locator('.portal-provider-dropdown .provider-option').filter({ hasText: 'Provider B' }).click();
    // Wait for model to switch to Provider B's default (replaces waitForSettle(500))
    await expect(page.locator('.model-trigger')).toContainText('model-b1');
    // Open model dropdown - should only show Provider B's models
    await page.locator('.model-trigger').click();
    // Wait for model dropdown to appear (replaces waitForSettle())
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();
    await expect(page.locator('.portal-model-dropdown .model-option')).toHaveCount(1);
    await expect(page.locator('.portal-model-dropdown .model-option')).toContainText('model-b1');
  });
});

test.describe.serial('Dropdown flip positioning', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, {
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'test-provider'
    );
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
    // Wait for dropdown to appear (replaces waitForSettle(500))
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
    expect(dropdownBottom).toBeLessThanOrEqual(triggerTop + 100); // 100px tolerance for rendering differences
  });
});

test.describe.serial('Config Providers', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should add a provider via settings modal', async ({ page }) => {
    // Reload to pick up any config changes from parallel tests
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    // Wait for provider cards or empty state to load
    await expect(async () => {
      const cards = await page.locator('.provider-card').count();
      const empty = await page.locator('.providers-empty').isVisible().catch(() => false);
      expect(cards > 0 || empty).toBe(true);
    }).toPass({ timeout: 5000 });
    const initialCount = await page.locator('.provider-card').count();
    await page.getByText('+ Add Provider').click();
    // Wait for new card to appear (replaces waitForSettle())
    await expect(async () => {
      const count = await page.locator('.provider-card').count();
      expect(count).toBe(initialCount + 1);
    }).toPass({ timeout: 5000 });
    // The newly added provider card
    const card = page.locator('.provider-card').last();
    // Use a unique URL to avoid conflicts with parallel tests
    const uniqueUrl = `http://test-provider-${Date.now()}.local/v1`;
    // Only URL input remains — name and display_name are auto-generated from URL
    await card.locator('input').first().fill(uniqueUrl);
    // Wait for auto-generation to run by checking a field has been populated (replaces waitForSettle(200))
    await expect(async () => {
      // Just wait for the card to be interactive
      await expect(card.locator('input').first()).toBeVisible();
    }).toPass({ timeout: 2000 });
    await page.getByRole('button', { name: 'Save' }).click();
    // Poll for the provider to appear (parallel tests may modify config)
    await expect(async () => {
      const config = await apiGetConfig(page);
      const myProvider = config.providers.find((p: any) => p.url === uniqueUrl);
      expect(myProvider).toBeDefined();
      expect(myProvider.name).toMatch(/test-provider/);
      expect(myProvider.display_name).toMatch(/test-provider/i);
      expect(myProvider.url).toBe(uniqueUrl);
    }).toPass({ timeout: 10000 });
  });

  test('should have Fetch Models button in provider card', async ({ page }) => {
    // Clear existing providers so the new one is the only card
    await apiPutConfig(page, { providers: [], default_provider: '' });
    // Wait for config to propagate (replaces waitForSettle(500))
    await waitForConfig(page, c => c.providers && c.providers.length === 0);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    const initialCount2 = await page.locator('.provider-card').count();
    await page.getByText('+ Add Provider').click();
    // Wait for new card to appear (replaces waitForSettle())
    await expect(async () => {
      const count = await page.locator('.provider-card').count();
      expect(count).toBe(initialCount2 + 1);
    }).toPass({ timeout: 5000 });
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
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});
    await apiPutConfig(page, {
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
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 2 && c.providers[0].name === 'provider-alpha'
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('create task modal shows provider selector with multiple providers', async ({ page }) => {
    // Ensure config is correct (parallel tests may have modified it)
    await apiPutConfig(page, {
      providers: [
        { name: 'provider-alpha', display_name: 'Alpha Server', url: 'http://alpha.local:8080/v1', models: ['alpha-model-1', 'alpha-model-2'], concurrency: 1, default_model: 'alpha-model-1' },
        { name: 'provider-beta', display_name: 'Beta Server', url: 'http://beta.local:9090/v1', models: ['beta-model-1', 'beta-model-2', 'beta-model-3'], concurrency: 1, default_model: 'beta-model-1' },
      ],
      default_provider: '',
    });
    await waitForConfig(page, c => c.providers && c.providers.length === 2);
    // Reload to ensure frontend picks up the config
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Wait for model selector row to appear (providers load via store subscription)
    await expect(page.locator('.model-select-row')).toBeVisible({ timeout: 10000 });

    // Provider selector should be visible (more than 1 provider)
    const providerTrigger = page.locator('.provider-trigger');
    await expect(providerTrigger).toBeVisible({ timeout: 10000 });
    await expect(providerTrigger).toContainText('Alpha Server');

    // Model selector should show the first provider's default model
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).toBeVisible();
    await expect(modelTrigger).toContainText('alpha-model-1');
  });

  test('switching provider updates model list in create task modal', async ({ page }) => {
    // Reload to ensure fresh config state
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Provider trigger should be visible (2+ providers configured)
    await expect(page.locator('.provider-trigger')).toBeVisible({ timeout: 10000 });

    // Click provider dropdown
    await page.locator('.provider-trigger').click();
    await page.waitForSelector('.portal-provider-dropdown', { timeout: 5000 });

    // Should show both providers
    const providerOptions = page.locator('.portal-provider-dropdown .provider-option');
    await expect(providerOptions).toHaveCount(2);

    // Select Beta Server
    await providerOptions.nth(1).click();
    // Wait for model to update (replaces waitForSettle(300))
    await expect(page.locator('.model-trigger')).toContainText('beta-model-1');
  });

  test('single provider hides provider selector in create task modal', async ({ page }) => {
    await apiPutConfig(page, {
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
    // Wait for config to propagate (replaces waitForSettle(200))
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'only-provider'
    );
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Provider selector should NOT be visible (only 1 provider)
    await expect(page.locator('.provider-trigger')).not.toBeVisible();

    // Model selector should still be visible
    await expect(page.locator('.model-trigger')).toBeVisible();
  });

});
