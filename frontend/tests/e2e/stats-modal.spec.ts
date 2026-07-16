import { test, expect } from '@playwright/test';
import {
  getProjectId,
  createTaskViaApi,
  csrfFetch,
  apiGetConfig,
  apiPutConfig,
  waitForConfig,
} from './helpers';

test.describe.configure({ mode: 'parallel' });

/**
 * Helper: open the stats modal from the top bar (desktop).
 * Returns true if the modal was opened successfully.
 */
async function openStatsModal(page: any): Promise<boolean> {
  // The stats button is in the top bar with aria-label="Token usage stats"
  // Use .top-bar scope to avoid strict mode violation (sidebar also has this button)
  const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
  if (!(await statsBtn.isVisible().catch(() => false))) {
    return false;
  }
  await statsBtn.click();
  await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
  // Verify the modal title
  await expect(page.locator('.modal-header h2')).toContainText('Token Usage');
  return true;
}

/**
 * Helper: close the stats modal via the close button.
 */
async function closeStatsModal(page: any): Promise<void> {
  await page.locator('.close-btn').click();
  await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
}

// ─── Test: Empty State ───

test('should show empty state when no token usage data exists', async ({ page }) => {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  await page.goto(`/?project=${projectId}`);
  await page.waitForSelector('.card', { timeout: 10000 });

  // Verify the top bar is visible (required for the stats button)
  const topBar = page.locator('.top-bar');
  await expect(topBar).toBeVisible({ timeout: 5000 });

  // Click the stats button in the top bar (desktop only)
  const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
  await expect(statsBtn).toBeVisible({ timeout: 5000 });
  await statsBtn.click();
  await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
  await expect(page.locator('.modal-header h2')).toContainText('Token Usage');

  // Wait for loading to finish
  await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

  // With no LLM messages, the empty state message should appear
  await expect(page.locator('.empty')).toContainText('No token usage data yet');

  await closeStatsModal(page);
});

// ─── Test: Close Modal ───

test('should close the modal when clicking the close button', async ({ page }) => {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  await page.goto(`/?project=${projectId}`);
  await page.waitForSelector('.card', { timeout: 10000 });

  const opened = await openStatsModal(page);
  if (!opened) {
    test.skip();
    return;
  }

  await closeStatsModal(page);
});

test('should close the modal when pressing Escape', async ({ page }) => {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  const projectId = await getProjectId(page);
  await page.goto(`/?project=${projectId}`);
  await page.waitForSelector('.card', { timeout: 10000 });

  const opened = await openStatsModal(page);
  if (!opened) {
    test.skip();
    return;
  }

  // Press Escape to close
  await page.keyboard.press('Escape');
  await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
});

// ─── Tests that require token stats data (serial for state sharing) ───
// Instead of relying on the executor (which may not start in test env),
// we create assistant messages directly via the API to populate stats.

test.describe.serial('Stats Modal with Token Data', () => {
  let projectId: string;
  let taskId: string;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `stats-test-${Date.now()}`);

    // Create an assistant message directly via API to populate token stats.
    // This bypasses the executor (which may not start in test env) while still
    // triggering the cumulative stats accumulation in project_token_stats.
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: {
        role: 'assistant',
        content: 'This is a test response for stats.',
        model_name: 'mock-model',
        provider_name: 'mock',
        prompt_tokens: 45,
        predicted_tokens: 8,
        cache_tokens: 12,
        prompt_ms: 320,
        predicted_ms: 180,
      },
    });
    if (!result.ok) {
      throw new Error(`Failed to create message: ${result.status} ${JSON.stringify(result.data)}`);
    }
  });

  test('should display token counts after an LLM response', async ({ page }) => {
    // Navigate to project view and open stats modal
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.modal-header h2')).toContainText('Token Usage');

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    // Summary card should be visible (not empty state)
    await expect(page.locator('.summary-card')).toBeVisible();

    // Verify summary labels exist (scoped to summary card to avoid table header matches)
    const summaryCard = page.locator('.summary-card');
    await expect(summaryCard.getByText('Prompt', { exact: true })).toBeVisible();
    await expect(summaryCard.getByText('Generated', { exact: true })).toBeVisible();
    await expect(summaryCard.getByText('Cached', { exact: true })).toBeVisible();

    // Verify token values are non-zero for prompt and generated
    // We set prompt_tokens: 45, predicted_tokens: 8
    const promptValue = page.locator('.summary-card').getByText('Prompt', { exact: true }).locator('..').locator('.summary-value');
    const generatedValue = page.locator('.summary-card').getByText('Generated', { exact: true }).locator('..').locator('.summary-value');

    const promptText = await promptValue.textContent();
    const generatedText = await generatedValue.textContent();

    // Parse numbers (may have locale formatting like commas)
    const promptNum = parseInt(promptText?.replace(/,/g, '') || '0', 10);
    const generatedNum = parseInt(generatedText?.replace(/,/g, '') || '0', 10);

    expect(promptNum).toBeGreaterThan(0);
    expect(generatedNum).toBeGreaterThan(0);

    await closeStatsModal(page);
  });

  test('should show per-provider breakdown in the model table', async ({ page }) => {
    // Navigate to project view and open stats modal
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    // Model table should be visible
    const table = page.locator('.model-table');
    await expect(table).toBeVisible();

    // Table should have at least one data row
    const rows = table.locator('tbody tr');
    await expect(rows.first()).toBeVisible();

    // The model label should contain the provider/model info
    const modelLabel = rows.first().locator('.model-label');
    await expect(modelLabel).toBeVisible();
    const labelText = await modelLabel.textContent();
    expect(labelText).not.toBeNull();
    expect(labelText!.length).toBeGreaterThan(0);

    // Verify table columns exist (use exact match to avoid "Prompt Speed" matching "Prompt")
    await expect(table.locator('thead th').filter({ hasText: 'Model' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Tasks' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Cached' })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: /^Prompt$/ })).toBeVisible();
    await expect(table.locator('thead th').filter({ hasText: 'Generated' })).toBeVisible();

    await closeStatsModal(page);
  });

  test('should display cache tokens separately', async ({ page }) => {
    // Navigate to project view and open stats modal
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    // The summary should show a "Cached" value
    const cachedLabel = page.locator('.summary-card').getByText('Cached', { exact: true });
    await expect(cachedLabel).toBeVisible();

    // The cached value should be present (we set cache_tokens: 12 in beforeEach)
    const cachedValue = cachedLabel.locator('..').locator('.summary-value');
    await expect(cachedValue).toBeVisible();
    const cachedText = await cachedValue.textContent();
    const cachedNum = parseInt(cachedText?.replace(/,/g, '') || '0', 10);
    // Value accumulates across serial tests, so just check it's > 0
    expect(cachedNum).toBeGreaterThan(0);

    // The model table should also have a "Cached" column
    const table = page.locator('.model-table');
    const cachedHeader = table.locator('thead th').filter({ hasText: 'Cached' });
    await expect(cachedHeader).toBeVisible();

    // The first data row should have a cached value cell
    const firstRow = table.locator('tbody tr').first();
    // Cached is the 3rd column (Model, Tasks, Cached)
    const cachedCell = firstRow.locator('td').nth(2);
    await expect(cachedCell).toBeVisible();

    await closeStatsModal(page);
  });
});

// ─── Cost Calculation Test ───
// This test modifies the config to add pricing and power settings.
// It saves and restores the original config to avoid affecting other tests.

test.describe.serial('Stats Modal Cost Calculation', () => {
  let projectId: string;
  let taskId: string;
  let originalConfig: any;

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    projectId = await getProjectId(page);
    taskId = await createTaskViaApi(page, projectId, `stats-cost-${Date.now()}`);

    // Save original config
    originalConfig = await apiGetConfig(page);

    // Configure provider with pricing and power settings
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.name === config.default_provider);
    if (provider) {
      provider.input_price = 3.0;       // $3 per 1M input tokens
      provider.output_price = 5.0;      // $5 per 1M output tokens
      provider.cache_read_price = 1.0;  // $1 per 1M cache tokens
      provider.power_consumption_w = 500;       // 500W
      provider.electricity_cost_per_kwh = 0.12; // $0.12/kWh
    }
    await apiPutConfig(page, config);
    await waitForConfig(page, (c) =>
      c.providers?.some((p: any) => p.input_price === 3.0 && p.power_consumption_w === 500)
    , 5000);

    // Create an assistant message with token stats
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/messages`, {
      method: 'POST',
      body: {
        role: 'assistant',
        content: 'Test response for cost calculation.',
        model_name: 'mock-model',
        provider_name: provider?.name || 'mock',
        prompt_tokens: 1000,
        predicted_tokens: 200,
        cache_tokens: 500,
        prompt_ms: 500,
        predicted_ms: 1000,
      },
    });
    if (!result.ok) {
      throw new Error(`Failed to create message: ${result.status} ${JSON.stringify(result.data)}`);
    }
  });

  test.afterEach(async ({ page }) => {
    // Restore original config
    if (originalConfig) {
      await apiPutConfig(page, originalConfig);
      await waitForConfig(page, (c) => c.providers?.length === originalConfig.providers?.length, 5000);
    }
  });

  test('should show cost column with calculated values when pricing is configured', async ({ page }) => {
    // Navigate to project view and open stats modal
    await page.goto(`/?project=${projectId}`);
    await page.waitForSelector('.card', { timeout: 10000 });

    // Verify the top bar is visible
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });

    // Click the stats button
    const statsBtn = page.locator('.top-bar [aria-label="Token usage stats"]');
    await expect(statsBtn).toBeVisible({ timeout: 5000 });
    await statsBtn.click();
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });

    // Wait for loading to finish
    await expect(page.locator('.loading')).not.toBeVisible({ timeout: 5000 }).catch(() => {});

    // Summary card should show cost
    const summaryCard = page.locator('.summary-card');
    const costLabel = summaryCard.getByText('Est. Total Cost');
    await expect(costLabel).toBeVisible();

    // The cost value should be non-zero (we configured pricing)
    const costValue = costLabel.locator('..').locator('.summary-value');
    await expect(costValue).toBeVisible();
    const costText = await costValue.textContent();
    // Cost should show a dollar amount (not "-")
    expect(costText).not.toBe('-');
    expect(costText).toContain('$');

    // The model table should also show cost
    const table = page.locator('.model-table');
    const costHeader = table.locator('thead th').filter({ hasText: 'Est. Total Cost' });
    await expect(costHeader).toBeVisible();

    // The cost header should have a tooltip
    await expect(costHeader).toHaveAttribute('title', 'Includes API pricing and electricity cost (when configured)');

    // Power column should show a value (not "-")
    const powerHeader = table.locator('thead th').filter({ hasText: 'Power' });
    await expect(powerHeader).toBeVisible();

    // First row's power cell should show a value
    const firstRow = table.locator('tbody tr').first();
    // Power is the 8th column (Model, Tasks, Cached, Prompt, Generated, Prompt Speed, Gen Speed, Power)
    const powerCell = firstRow.locator('td').nth(7);
    await expect(powerCell).toBeVisible();
    const powerText = await powerCell.textContent();
    // Power should show a value (not "-")
    expect(powerText).not.toBe('-');

    await closeStatsModal(page);
  });
});
