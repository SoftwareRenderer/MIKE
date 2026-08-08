import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig } from './helpers';

// ─── Helpers ────────────────────────────────────────────────────────────────

const BASE_CONFIG = {
  workspace_dir: './.worktrees',
  default_auto_commit: false,
  default_plan_mode: false,
  default_max_failed_turns: 3,
};

/** Provider with BOTH provider-level defaults and per-model overrides. */
function modelOverrideConfig() {
  return {
    ...BASE_CONFIG,
    providers: [
      {
        name: 'test-localhost-9999',
        display_name: 'Test Server',
        url: 'http://localhost:9999/v1',
        models: [
          {
            name: 'model-a',
            reasoning_effort: 'medium',
            context_window: 32768,
            input_price: 1.5,
            output_price: 2.5,
            cache_read_price: 0.5,
            cache_write_price: 0,
            power_consumption_w: 350,
          },
        ],
        concurrency: 1,
        input_price: 3.0,
        output_price: 5.0,
        cache_read_price: 1.0,
        power_consumption_w: 500,
        electricity_cost_per_kwh: 0.12,
      },
    ],
    worker_provider: 'test-localhost-9999',
    worker_model: 'model-a',
    utility_provider: 'test-localhost-9999',
    utility_model: 'model-a',
  };
}

/** Provider with ONLY provider-level defaults (models inherit). */
function providerDefaultsOnlyConfig() {
  return {
    ...BASE_CONFIG,
    providers: [
      {
        name: 'test-localhost-9999',
        display_name: 'Test Server',
        url: 'http://localhost:9999/v1',
        models: ['model-a'],
        concurrency: 1,
        input_price: 3.0,
        output_price: 5.0,
        cache_read_price: 1.0,
        power_consumption_w: 500,
        electricity_cost_per_kwh: 0.12,
      },
    ],
    worker_provider: 'test-localhost-9999',
    worker_model: 'model-a',
    utility_provider: 'test-localhost-9999',
    utility_model: 'model-a',
  };
}

async function openSettingsAndExpandProvider(page: import('@playwright/test').Page) {
  await page.keyboard.press('Escape').catch(() => {});
  await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

  await page.locator('.icon-btn[aria-label="Settings"]').click();
  await page.waitForSelector('.modal', { timeout: 5000 });

  const card = page.locator('.provider-card').first();
  await expect(card).toBeVisible();
  if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
    await card.locator('.provider-card-header').click();
    await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });
  }
  return card;
}

async function expandFirstModelRow(card: import('@playwright/test').Locator) {
  const expandBtn = card.locator('.model-row').first().locator('.btn-expand-model');
  await expandBtn.click();
  await expect(card.locator('.model-details')).toBeVisible({ timeout: 5000 });
}

// ─── Per-Model Pricing / Power / Context Tests ─────────────────────────────

test.describe.serial('Settings Model Pricing & Power', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, modelOverrideConfig());
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].models &&
      c.providers[0].models.length === 1 && c.providers[0].models[0].name === 'model-a'
    , 5000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should render per-model pricing, power override, and context fields', async ({ page }) => {
    const card = await openSettingsAndExpandProvider(page);

    // Model row exists with the context size pre-populated
    const ctxInput = card.locator('.ctx-input').first();
    await expect(ctxInput).toBeVisible();
    await expect(ctxInput).toHaveValue('32768');

    // Expand the model row to reveal pricing & power
    await expandFirstModelRow(card);

    // Per-model pricing fields show the configured values (override provider defaults)
    await expect(card.locator('#model-input-price-0-0')).toHaveValue('1.5');
    await expect(card.locator('#model-output-price-0-0')).toHaveValue('2.5');
    await expect(card.locator('#model-cache-read-price-0-0')).toHaveValue('0.5');
    await expect(card.locator('#model-cache-write-price-0-0')).toHaveValue('0');
    await expect(card.locator('#model-power-w-0-0')).toHaveValue('350');

    // Provider-level defaults are still present below
    await expect(card.locator('#power-w-0')).toHaveValue('500');
  });

  test('should save and persist per-model pricing, power, and context', async ({ page }) => {
    const card = await openSettingsAndExpandProvider(page);
    await expandFirstModelRow(card);

    // Edit per-model values
    await card.locator('#model-input-price-0-0').fill('9.99');
    await card.locator('#model-output-price-0-0').fill('10');
    await card.locator('#model-cache-read-price-0-0').fill('2');
    await card.locator('#model-cache-write-price-0-0').fill('1.25');
    await card.locator('#model-power-w-0-0').fill('750');
    await card.locator('.ctx-input').first().fill('65536');

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify via API
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    const model = provider.models.find((m: any) => m.name === 'model-a');
    expect(model).toBeDefined();
    expect(model.context_window).toBe(65536);
    expect(model.input_price).toBe(9.99);
    expect(model.output_price).toBe(10);
    expect(model.cache_read_price).toBe(2);
    expect(model.cache_write_price).toBe(1.25);
    expect(model.power_consumption_w).toBe(750);

    // Provider-level defaults unchanged
    expect(provider.input_price).toBe(3);
    expect(provider.output_price).toBe(5);
    expect(provider.power_consumption_w).toBe(500);
  });

  test('Fetch Models pre-populates the context size field', async ({ page }) => {
    const card = await openSettingsAndExpandProvider(page);

    // Intercept fetch-models to return a model with a context window
    await page.route('**/api/config/fetch-models', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ models: [{ name: 'model-a', context_window: 65536 }] }),
      });
    });

    await card.locator('.btn-fetch').click();
    // Wait for the model row to re-render with the fetched context
    await expect(card.locator('.ctx-input').first()).toHaveValue('65536', { timeout: 10000 });
  });

  test('re-fetching models preserves existing per-model pricing/power', async ({ page }) => {
    const card = await openSettingsAndExpandProvider(page);
    await expandFirstModelRow(card);
    await expect(card.locator('#model-input-price-0-0')).toHaveValue('1.5');

    // Re-fetch returns the same model with a fresh context window
    await page.route('**/api/config/fetch-models', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ models: [{ name: 'model-a', context_window: 8192 }] }),
      });
    });

    await card.locator('.btn-fetch').click();
    await expect(card.locator('.ctx-input').first()).toHaveValue('8192', { timeout: 10000 });

    // Row stays expanded after re-fetch; per-model pricing survives
    await expect(card.locator('#model-input-price-0-0')).toHaveValue('1.5');
    await expect(card.locator('#model-power-w-0-0')).toHaveValue('350');
  });
});

// ─── Inheritance (empty = provider default) Tests ─────────────────────────

test.describe.serial('Settings Model Pricing Inheritance', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(page, providerDefaultsOnlyConfig());
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].models &&
      c.providers[0].models.length === 1
    , 5000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('empty per-model fields show default placeholder and inherit provider pricing', async ({ page }) => {
    const card = await openSettingsAndExpandProvider(page);
    await expandFirstModelRow(card);

    // Per-model fields are empty with "default" placeholder
    const input = card.locator('#model-input-price-0-0');
    await expect(input).toHaveValue('');
    await expect(input).toHaveAttribute('placeholder', 'default');
    await expect(card.locator('#model-power-w-0-0')).toHaveValue('');

    // Context field placeholder is "auto" (empty = runtime auto-discovery)
    const ctxInput = card.locator('.ctx-input').first();
    await expect(ctxInput).toHaveValue('');
    await expect(ctxInput).toHaveAttribute('placeholder', 'auto');

    // Save without entering any per-model values
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Model persists WITHOUT per-model overrides (fields omitted → inherit provider)
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    const model = provider.models.find((m: any) => m.name === 'model-a');
    expect(model).toBeDefined();
    expect(model).not.toHaveProperty('input_price');
    expect(model).not.toHaveProperty('output_price');
    expect(model).not.toHaveProperty('power_consumption_w');
    // Provider defaults intact
    expect(provider.input_price).toBe(3);
    expect(provider.power_consumption_w).toBe(500);
  });
});
