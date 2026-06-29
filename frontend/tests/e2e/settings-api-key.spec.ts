import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig } from './helpers';

const PROVIDER_WITH_KEY = {
  name: 'test-localhost-9999',
  display_name: 'Test Server',
  url: 'http://localhost:9999/v1',
  models: ['model-a', 'model-b'],
  concurrency: 2,
  default_model: 'model-a',
};

/** Clear api_key in providers (explicitly set to empty string to clear, not omit to preserve). */
function clearAPIKeys(config: Record<string, any>): Record<string, any> {
  const cleared = { ...config };
  if (cleared.providers) {
    cleared.providers = cleared.providers.map((p: any) => ({
      ...p,
      api_key: '', // explicit empty = clear the key (nil = preserve)
    }));
  }
  return cleared;
}

const DEFAULT_CONFIG = {
  providers: [PROVIDER_WITH_KEY],
  default_provider: 'test-localhost-9999',
  workspace_dir: './.worktrees',
  default_auto_commit: false,
  default_plan_mode: false,
  default_max_failed_turns: 3,
};

test.describe.serial('Settings API Key', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

    await apiPutConfig(page, clearAPIKeys(DEFAULT_CONFIG));
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].url === 'http://localhost:9999/v1'
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show API key input on same line as URL', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // URL and API key are in the same row
    const row = card.locator('.url-and-key-row');
    await expect(row).toBeVisible();

    // API key input is visible and editable (not disabled)
    const apiKeyInput = card.locator('.api-key-field input');
    await expect(apiKeyInput).toBeVisible();
    expect(await apiKeyInput.isDisabled()).toBe(false);

    // Placeholder is ********
    await expect(apiKeyInput).toHaveAttribute('placeholder', 'API key...');

    // Label says "optional"
    const label = card.locator('.api-key-label');
    await expect(label).toBeVisible();
    await expect(label.locator('.optional-text')).toContainText('optional');
  });

  test('should show masked key when provider has an API key', async ({ page }) => {
    // Set up a provider with an API key via API
    await apiPutConfig(page, {
      providers: [{ ...PROVIDER_WITH_KEY, api_key: 'sk-test-1234567890abcdef' }],
      default_provider: 'test-localhost-9999',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].api_key && c.providers[0].api_key.length > 0
    );

    // Reload to pick up new config
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Input value is masked (10 bullets)
    const apiKeyInput = card.locator('.api-key-field input');
    const value = await apiKeyInput.inputValue();
    expect(value).toBe('••••••••••');
  });

  test('should allow entering and saving a new API key', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Enter a new key directly
    const apiKeyInput = card.locator('.api-key-field input');
    await apiKeyInput.fill('sk-new-api-key-xyz');
    await expect(apiKeyInput).toHaveValue('sk-new-api-key-xyz');

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify the key was saved (check via API that api_key is set, not its value)
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    // API returns masked key
    expect(provider.api_key).toBe('••••••••••');
  });

  test('GET /api/config returns masked api_key', async ({ page }) => {
    // Set up provider with API key
    await apiPutConfig(page, {
      providers: [{ ...PROVIDER_WITH_KEY, api_key: 'sk-secret-key-12345' }],
      default_provider: 'test-localhost-9999',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers[0].api_key && c.providers[0].api_key.length > 0
    );

    // GET /api/config should return masked key
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    // Masked key is 10 bullets
    expect(provider.api_key).toBe('••••••••••');
    // Real key should never appear
    expect(provider.api_key).not.toContain('sk-secret');
  });

  test('GET /api/config returns encryption_enabled field', async ({ page }) => {
    const config = await apiGetConfig(page);
    expect(config.encryption_enabled).toBeDefined();
    expect(typeof config.encryption_enabled).toBe('boolean');
  });

  test('provider without API key works (empty field is valid)', async ({ page}) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Save without setting an API key
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Config should still have the provider
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    expect(provider.url).toBe('http://localhost:9999/v1');
  });

  test('Test Connection passes the API key to the backend', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Enter an API key
    const apiKeyInput = card.locator('.api-key-field input');
    await apiKeyInput.fill('sk-test-connection-key');

    // Intercept the test-connection API call to verify the API key is sent
    await page.route('**/api/config/test-connection', async (route, request) => {
      const body = JSON.parse(request.postData() || '{}');
      expect(body.api_key).toBe('sk-test-connection-key');
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ success: true, message: 'Connection successful!' }),
      });
    });

    // Click Test Connection
    const testBtn = card.locator('.btn-test');
    await testBtn.click();

    // Wait for result
    await expect(card.locator('.test-result')).toBeVisible({ timeout: 10000 });
  });

  test('Fetch Models passes the API key to the backend', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Enter an API key
    const apiKeyInput = card.locator('.api-key-field input');
    await apiKeyInput.fill('sk-fetch-models-key');

    // Intercept the fetch-models API call
    await page.route('**/api/config/fetch-models', async (route, request) => {
      const body = JSON.parse(request.postData() || '{}');
      expect(body.api_key).toBe('sk-fetch-models-key');
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ models: [{ name: 'test-model' }] }),
      });
    });

    // Click Fetch Models
    const fetchBtn = card.locator('.btn-fetch');
    await fetchBtn.click();

    // Wait for model rows to appear
    await expect(card.locator('.model-row')).toHaveCount(1, { timeout: 10000 });
  });

  test('clearing API key input removes the key on save', async ({ page }) => {
    // Set up provider with API key
    await apiPutConfig(page, {
      providers: [{ ...PROVIDER_WITH_KEY, api_key: 'sk-existing-key' }],
      default_provider: 'test-localhost-9999',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers[0].api_key && c.providers[0].api_key.length > 0
    );

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Clear the masked key and enter empty
    const apiKeyInput = card.locator('.api-key-field input');
    await apiKeyInput.fill('');

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify the key was cleared
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    expect(provider.api_key).toBe('');
  });
});
