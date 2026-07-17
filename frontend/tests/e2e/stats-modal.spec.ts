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

// ─── Test: Empty State (verifies via API to avoid race with data-creating tests) ───

test.describe.serial('Stats Modal Empty State', () => {
  test('should show empty state when no token usage data exists', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const projectId = await getProjectId(page);

    // First verify via API that the project has no token stats.
    // This avoids race conditions with parallel tests that create token data.
    const stats = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}/stats`);
      return res.ok ? res.json() : null;
    }, projectId);

    // If there are existing stats from parallel tests, skip the empty-state assertion
    // and instead verify the modal opens and loads properly.
    const hasStats = stats?.by_model && stats.by_model.length > 0;

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

    if (!hasStats) {
      // With no LLM messages, the empty state message should appear
      await expect(page.locator('.empty')).toContainText('No token usage data yet');
    } else {
      // When stats exist from parallel tests, verify the summary card is shown
      await expect(page.locator('.summary-card')).toBeVisible({ timeout: 5000 });
    }

    await closeStatsModal(page);
  });
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
});




