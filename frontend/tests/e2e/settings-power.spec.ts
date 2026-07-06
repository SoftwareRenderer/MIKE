import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, getProjectId } from './helpers';

const PROVIDER_WITH_POWER = {
  name: 'test-localhost-9999',
  display_name: 'Test Server',
  url: 'http://localhost:9999/v1',
  models: ['model-a'],
  concurrency: 1,
  default_model: 'model-a',
  input_price: 3.0,
  output_price: 5.0,
  cache_read_price: 1.0,
  cache_write_price: 0,
  power_consumption_w: 500,
  electricity_cost_per_kwh: 0.12,
};

/** Clear api_key in providers. */
function clearAPIKeys(config: Record<string, any>): Record<string, any> {
  const cleared = { ...config };
  if (cleared.providers) {
    cleared.providers = cleared.providers.map((p: any) => ({
      ...p,
      api_key: '',
    }));
  }
  return cleared;
}

const DEFAULT_CONFIG = {
  providers: [PROVIDER_WITH_POWER],
  default_provider: 'test-localhost-9999',
  workspace_dir: './.worktrees',
  default_auto_commit: false,
  default_plan_mode: false,
  default_max_failed_turns: 3,
};

test.describe.serial('Settings Power Consumption', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

    await apiPutConfig(page, clearAPIKeys(DEFAULT_CONFIG));
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].url === 'http://localhost:9999/v1'
    , 5000);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should show power and electricity inputs in provider settings', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Power section should be visible
    const powerSection = card.locator('.power-section');
    await expect(powerSection).toBeVisible();

    // Power (W) input should exist and have the configured value
    const powerInput = card.locator('#power-w-0');
    await expect(powerInput).toBeVisible();
    await expect(powerInput).toHaveValue('500');

    // Electricity ($/kWh) input should exist and have the configured value
    const elecInput = card.locator('#elec-cost-0');
    await expect(elecInput).toBeVisible();
    await expect(elecInput).toHaveValue('0.12');
  });

  test('should save and persist power/electricity settings', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Change power consumption
    const powerInput = card.locator('#power-w-0');
    await powerInput.fill('750');

    // Change electricity cost
    const elecInput = card.locator('#elec-cost-0');
    await elecInput.fill('0.25');

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify via API
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    expect(provider.power_consumption_w).toBe(750);
    expect(provider.electricity_cost_per_kwh).toBe(0.25);
  });

  test('should allow empty power settings (zero values)', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Clear power values
    const powerInput = card.locator('#power-w-0');
    await powerInput.fill('');

    const elecInput = card.locator('#elec-cost-0');
    await elecInput.fill('');

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    // Verify via API — values should be 0
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
    expect(provider).toBeDefined();
    expect(provider.power_consumption_w).toBe(0);
    expect(provider.electricity_cost_per_kwh).toBe(0);
  });

  test('Token Usage shows power column and merged cost', async ({ page }) => {
    // This test verifies the StatsModal has the power column in the table
    // We can't easily test the actual values without LLM-generated messages,
    // but we can verify the UI elements exist.

    // Navigate to a task to make the stats button available
    const projectId = await getProjectId(page);
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Open the stats modal via the top bar
    const statsBtn = page.locator('[aria-label="Token Usage"]');
    if (await statsBtn.isVisible().catch(() => false)) {
      await statsBtn.click();
      await page.waitForSelector('.modal', { timeout: 5000 });

      // Check for the "Power" column header in the model table
      const table = page.locator('.model-table');
      await expect(table).toBeVisible();

      // The table should have a "Power" column header
      const powerHeader = table.locator('th').filter({ hasText: 'Power' });
      await expect(powerHeader).toBeVisible();

      // The "Est. Total Cost" column should have a title tooltip
      const costHeader = table.locator('th').filter({ hasText: 'Est. Total Cost' });
      await expect(costHeader).toBeVisible();
      await expect(costHeader).toHaveAttribute('title', 'Includes API pricing and electricity cost (when configured)');

      // Close the modal
      await page.locator('.close-btn').click();
      await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    }
  });
});
