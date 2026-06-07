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

test.describe.serial('Settings Save', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await waitForSettle(200);

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
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('modal should close after save', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await expect(page.locator('.modal')).toBeVisible();

    // Click Save
    await page.getByRole('button', { name: 'Save' }).click();

    // Modal should close within reasonable time (proves "Saving..." doesn't get stuck)
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });
  });

  test('save should persist config changes', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Toggle Auto-commit
    await page.locator('.toggle-switch').first().click();
    await waitForSettle(200);

    // Change max failed turns
    await page.locator('#default-max-failed').fill('7');
    await waitForSettle(200);

    // Click Save
    await page.getByRole('button', { name: 'Save' }).click();

    // Wait for modal to close
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify config was saved
    const config = await apiGetConfig();
    expect(config.default_auto_commit).toBe(true);
    expect(config.default_max_failed_turns).toBe(7);
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

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Card should be expanded (no default_model set on this fresh provider... wait, it has default_model)
    // Cards with default_model start collapsed. Click header to expand.
    const card = page.locator('.provider-card').first();
    await card.locator('.provider-card-header').click();
    await waitForSettle(500);

    // Verify default model badge exists
    await expect(card.locator('.btn-default.active')).toBeVisible({ timeout: 5000 });

    // Remove model-a (first model row after sort)
    await card.locator('.btn-remove-model').first().click();
    await waitForSettle(500);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify default_model is cleared
    // Note: syncProviderMeta regenerates name from URL: example.com:80 → "example-com-80"
    const config = await apiGetConfig();
    const provider = config.providers.find((p: any) => p.url === 'http://example.com/v1');
    expect(provider).toBeDefined();
    expect(provider.default_model).toBe('');
  });
});
