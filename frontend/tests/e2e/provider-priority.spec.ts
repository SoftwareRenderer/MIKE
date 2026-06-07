import { test, expect } from '@playwright/test';
import http from 'node:http';
import { getProjectId } from './helpers';

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

const multiProviderConfig = {
  providers: [
    { name: 'provider-alpha', display_name: 'Alpha', url: 'http://alpha.local/v1', models: ['alpha-model-1', 'alpha-model-2'], default_model: '' },
    { name: 'provider-beta', display_name: 'Beta', url: 'http://beta.local/v1', models: ['beta-model-1', 'beta-model-2'], default_model: '' },
  ],
};

test.describe.serial('Provider Priority and Per-Provider Default Model', () => {
  test.beforeEach(async ({ page }) => {
    await apiPutConfig(multiProviderConfig);
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('settings: up/down arrows reorder providers', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await expect(page.locator('.provider-card')).toHaveCount(2);

    // Verify initial order: Alpha first, Beta second
    const labels = page.locator('.provider-meta');
    await expect(labels.first()).toContainText('Alpha');
    await expect(labels.last()).toContainText('Beta');

    // Move Alpha down (click down arrow on first provider)
    const firstCard = page.locator('.provider-card').first();
    const downArrow = firstCard.locator('.btn-reorder-down');
    await expect(downArrow).toBeVisible();
    await downArrow.click();
    await waitForSettle(300);

    // Order should now be: Beta first, Alpha second
    await expect(labels.first()).toContainText('Beta');
    await expect(labels.last()).toContainText('Alpha');

    // Move Alpha back up (click up arrow on LAST provider, which is now Alpha)
    const lastCard = page.locator('.provider-card').last();
    const upArrow = lastCard.locator('.btn-reorder-up');
    await expect(upArrow).toBeVisible();
    await expect(upArrow).toBeEnabled();
    await upArrow.click();
    await waitForSettle(300);

    // Back to original order
    await expect(labels.first()).toContainText('Alpha');
    await expect(labels.last()).toContainText('Beta');

    // Save and verify order persisted
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    const config = await apiGetConfig();
    expect(config.providers[0].url).toBe('http://alpha.local/v1');
    expect(config.providers[1].url).toBe('http://beta.local/v1');
  });

  test('settings: up arrow disabled on first provider, down arrow disabled on last', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const firstCard = page.locator('.provider-card').first();
    const lastCard = page.locator('.provider-card').last();

    // First provider: up arrow disabled, down arrow enabled
    await expect(firstCard.locator('.btn-reorder-up')).toBeDisabled();
    await expect(firstCard.locator('.btn-reorder-down')).toBeEnabled();

    // Last provider: up arrow enabled, down arrow disabled
    await expect(lastCard.locator('.btn-reorder-up')).toBeEnabled();
    await expect(lastCard.locator('.btn-reorder-down')).toBeDisabled();
  });

  test('settings: star button sets per-provider default model', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'star-test', display_name: 'Star Test', url: 'http://star.local/v1', models: ['model-x', 'model-y'], default_model: '' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    const modelRows = card.locator('.model-row');
    await expect(modelRows).toHaveCount(2);

    // Find star button for model-y (2nd row)
    const yRow = modelRows.nth(1);
    const yStar = yRow.locator('.btn-star');
    await expect(yStar).toBeVisible();
    await expect(yStar).not.toHaveClass(/active/);

    // Click star on model-y
    await yStar.click();
    await waitForSettle(200);

    // model-y star should be active
    await expect(yStar).toHaveClass(/active/);

    // model-x star should NOT be active (only one default per provider)
    const xRow = modelRows.nth(0);
    const xStar = xRow.locator('.btn-star');
    await expect(xStar).not.toHaveClass(/active/);

    // Save and verify
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    const config = await apiGetConfig();
    expect(config.providers[0].default_model).toBe('model-y');
  });

  test('settings: toggling star off clears per-provider default', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'toggle-test', display_name: 'Toggle Test', url: 'http://toggle.local/v1', models: ['model-a', 'model-b'], default_model: 'model-a' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    const aRow = card.locator('.model-row').nth(0);
    const aStar = aRow.locator('.btn-star');
    await expect(aStar).toHaveClass(/active/);

    // Toggle off
    await aStar.click();
    await waitForSettle(200);
    await expect(aStar).not.toHaveClass(/active/);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    const config = await apiGetConfig();
    expect(config.providers[0].default_model).toBe('');
  });

  test('settings: each provider can have its own default model independently', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'ind-alpha', display_name: 'Ind Alpha', url: 'http://ind-alpha.local/v1', models: ['a1', 'a2'], default_model: 'a1' },
        { name: 'ind-beta', display_name: 'Ind Beta', url: 'http://ind-beta.local/v1', models: ['b1', 'b2'], default_model: 'b2' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Alpha card: a1 should be starred
    const alphaCard = page.locator('.provider-card').nth(0);
    await expect(alphaCard.locator('.model-row').nth(0).locator('.btn-star')).toHaveClass(/active/);
    await expect(alphaCard.locator('.model-row').nth(1).locator('.btn-star')).not.toHaveClass(/active/);

    // Beta card: b2 should be starred
    const betaCard = page.locator('.provider-card').nth(1);
    await expect(betaCard.locator('.model-row').nth(0).locator('.btn-star')).not.toHaveClass(/active/);
    await expect(betaCard.locator('.model-row').nth(1).locator('.btn-star')).toHaveClass(/active/);

    // Save and verify both defaults persist
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    const config = await apiGetConfig();
    expect(config.providers[0].default_model).toBe('a1');
    expect(config.providers[1].default_model).toBe('b2');
  });

  test('CreateTaskModal: pre-selects highest-priority provider default model', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'prio-alpha', display_name: 'Prio Alpha', url: 'http://prio-alpha.local/v1', models: ['alpha-1', 'alpha-2'], default_model: 'alpha-2' },
        { name: 'prio-beta', display_name: 'Prio Beta', url: 'http://prio-beta.local/v1', models: ['beta-1', 'beta-2'], default_model: 'beta-1' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open create task modal
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Should pre-select alpha-2 (first provider's default)
    await expect(page.locator('.model-selector')).toBeVisible();
    await expect(page.locator('.model-name')).toContainText('alpha-2');
  });

  test('CreateTaskModal: falls back to first model if no default set', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'fallback-alpha', display_name: 'Fallback Alpha', url: 'http://fallback-alpha.local/v1', models: ['fb-1', 'fb-2'], default_model: '' },
        { name: 'fallback-beta', display_name: 'Fallback Beta', url: 'http://fallback-beta.local/v1', models: ['fb-3'], default_model: 'fb-3' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // First provider has no default_model, so fallback to first available model (fb-1)
    await expect(page.locator('.model-name')).toContainText('fb-1');
  });

  test('CreateTaskModal: reordering providers changes pre-selected model', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'reorder-a', display_name: 'Reorder A', url: 'http://reorder-a.local/v1', models: ['ra-1'], default_model: 'ra-1' },
        { name: 'reorder-b', display_name: 'Reorder B', url: 'http://reorder-b.local/v1', models: ['rb-1'], default_model: 'rb-1' },
      ],
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open settings, reorder: move Reorder A down so B is first
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const firstCard = page.locator('.provider-card').first();
    await firstCard.locator('.btn-reorder-down').click();
    await waitForSettle(300);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    // Reload page to pick up config change
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open create task modal — should now pre-select rb-1 (B is now first)
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });
    await expect(page.locator('.model-name')).toContainText('rb-1');
  });
});
