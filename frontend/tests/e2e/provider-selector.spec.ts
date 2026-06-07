import { test, expect } from '@playwright/test';
import http from 'node:http';

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

test.describe.serial('Provider Selector', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await waitForSettle(200);

    // Set up two providers with different models
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
      workspace_dir: './worktrees',
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('create task modal shows provider selector with multiple providers', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Provider selector should be visible (more than 1 provider)
    const providerTrigger = page.locator('.provider-trigger');
    await expect(providerTrigger).toBeVisible();

    // Should show the first provider's display name (highest priority)
    await expect(providerTrigger).toContainText('Alpha Server');

    // Model selector should show the first provider's default model
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).toBeVisible();
    await expect(modelTrigger).toContainText('alpha-model-1');
  });

  test('switching provider updates model list', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Click provider dropdown
    await page.locator('.provider-trigger').click();
    await page.waitForSelector('.portal-provider-dropdown', { timeout: 5000 });

    // Should show both providers
    const providerOptions = page.locator('.portal-provider-dropdown .provider-option');
    await expect(providerOptions).toHaveCount(2);
    await expect(providerOptions.nth(0)).toContainText('Alpha Server');
    await expect(providerOptions.nth(1)).toContainText('Beta Server');

    // Select Beta Server
    await providerOptions.nth(1).click();
    await waitForSettle(300);

    // Model selector should now show beta-model-1 (Beta's default)
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).toContainText('beta-model-1');

    // Provider trigger should show Beta Server
    await expect(page.locator('.provider-trigger')).toContainText('Beta Server');
  });

  test('model dropdown shows only selected provider\'s models', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Switch to Beta provider
    await page.locator('.provider-trigger').click();
    await page.waitForSelector('.portal-provider-dropdown', { timeout: 5000 });
    await page.locator('.portal-provider-dropdown .provider-option').filter({ hasText: 'Beta Server' }).click();
    await waitForSettle(300);

    // Open model dropdown
    await page.locator('.model-trigger').click();
    await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });

    // Should only show Beta's models (3 models)
    const modelOptions = page.locator('.portal-model-dropdown .model-option');
    await expect(modelOptions).toHaveCount(3);
    await expect(modelOptions.nth(0)).toContainText('beta-model-1');
    await expect(modelOptions.nth(1)).toContainText('beta-model-2');
    await expect(modelOptions.nth(2)).toContainText('beta-model-3');

    // Should NOT show Alpha's models
    for (let i = 0; i < 3; i++) {
      const text = await modelOptions.nth(i).textContent();
      expect(text).not.toContain('alpha');
    }
  });

  test('single provider hides provider selector', async ({ page }) => {
    // Reset to single provider
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
    const providerTrigger = page.locator('.provider-trigger');
    await expect(providerTrigger).not.toBeVisible();

    // Model selector should still be visible
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).toBeVisible();
  });

  test('selecting a model from dropdown closes it', async ({ page }) => {
    await page.getByText('+ Add Task').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Open model dropdown
    await page.locator('.model-trigger').click();
    await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });
    await expect(page.locator('.portal-model-dropdown')).toBeVisible();

    // Select a model
    await page.locator('.portal-model-dropdown .model-option').nth(1).click();
    await waitForSettle(300);

    // Dropdown should close
    await expect(page.locator('.model-dropdown')).not.toBeVisible();

    // Model trigger should show selected model
    await expect(page.locator('.model-trigger')).toContainText('alpha-model-2');
  });
});
