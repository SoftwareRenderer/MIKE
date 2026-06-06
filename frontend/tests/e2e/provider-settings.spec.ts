import { test, expect } from '@playwright/test';
import http from 'node:http';
import { getProjectId, createTaskViaApi } from './helpers';

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

test.describe.serial('Provider Settings', () => {
  test.beforeEach(async ({ page }) => {
    // Reset providers to a known state
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] },
      ],
      default_provider: '',
    });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should not show legacy fallback section in settings modal', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Legacy section should not exist
    await expect(page.locator('.legacy-section')).not.toBeVisible();

    // "Legacy (fallback)" label should not exist
    const legacyLabel = page.getByText('Legacy (fallback)');
    await expect(legacyLabel).not.toBeVisible();

    // "Llama.cpp Server URL" label should not exist
    const llamaLabel = page.getByText('Llama.cpp Server URL');
    await expect(llamaLabel).not.toBeVisible();
  });

  test('should not show default provider dropdown in settings modal', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // "Default Provider" label should not exist
    const defaultProviderLabel = page.getByText('Default Provider');
    await expect(defaultProviderLabel).not.toBeVisible();

    // No select element in the modal (the default provider dropdown was a <select>)
    const selectInModal = page.locator('.modal select');
    await expect(selectInModal).toHaveCount(0);
  });

  test('should not reference legacy fields in empty providers message', async ({ page }) => {
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // The empty message should not mention "legacy"
    const emptyMessage = page.locator('.providers-empty');
    if (await emptyMessage.isVisible().catch(() => false)) {
      const text = await emptyMessage.textContent();
      expect(text?.toLowerCase()).not.toContain('legacy');
    }
  });

  test('"Fetch Models" should clear existing models before populating new ones', async ({ page }) => {
    // Setup: provider with 2 existing models
    await apiPutConfig({
      providers: [
        { name: 'fetch-test', display_name: 'Fetch Test', url: 'http://example.com/v1', models: ['old-model-1', 'old-model-2'] },
      ],
      default_provider: '',
    });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Verify existing models are present
    const card = page.locator('.provider-card').first();
    await expect(card.locator('.model-row')).toHaveCount(2);

    // Mock the fetch-models API to return a different set of models
    await page.route('**/api/config/fetch-models', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ models: ['new-model-a', 'new-model-b', 'new-model-c'] }),
      });
    });

    // Click "Fetch Models"
    await card.locator('.btn-fetch').click();
    await waitForSettle(500);

    // Should now show exactly 3 new models (not 5 = 2 old + 3 new)
    await expect(card.locator('.model-row')).toHaveCount(3);

    // Fetched models are read-only (rendered as spans, not inputs) but still removable
    await expect(card.locator('.model-row input')).toHaveCount(0);
    const modelLabels = card.locator('.model-name-label');
    const values: string[] = [];
    for (let i = 0; i < 3; i++) {
      values.push(await modelLabels.nth(i).textContent());
    }
    expect(values).toContain('new-model-a');
    expect(values).toContain('new-model-b');
    expect(values).toContain('new-model-c');
    expect(values).not.toContain('old-model-1');
    expect(values).not.toContain('old-model-2');
    // Each readonly model has a remove button
    await expect(card.locator('.btn-remove-model')).toHaveCount(3);

    // Manually added models should be editable (inputs with remove buttons)
    await card.locator('.btn-add-model').click();
    await waitForSettle(200);
    await expect(card.locator('.model-row')).toHaveCount(4);
    // 3 readonly spans + 1 editable input
    await expect(card.locator('.model-name-label')).toHaveCount(3);
    await expect(card.locator('.model-row input')).toHaveCount(1);
    // 3 remove buttons for readonly + 1 for manual = 4 total
    await expect(card.locator('.btn-remove-model')).toHaveCount(4);
  });

  test('model selector in chat should update after saving new providers in settings', async ({ page }) => {
    // Start with no providers
    await apiPutConfig({ providers: [], default_provider: '' });
    await waitForSettle(500);
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `model-update-test-${Date.now()}`);

    // Navigate to task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await waitForSettle(2000); // wait for onMount

    // Click "Start Task" if visible (new task shows Start button, not ChatForm)
    const startBtn = page.locator('.btn-start-task');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click({ timeout: 5000 }).catch(() => {});
      await waitForSettle(1000);
    }
    // Wait for chat form
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Model selector should NOT be visible (no providers configured)
    const modelTrigger = page.locator('.model-trigger');
    await expect(modelTrigger).not.toBeVisible();

    // Open settings and add a provider
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    const card = page.locator('.provider-card').last();
    await card.locator('input').first().fill('http://newprovider.local:9999/v1');
    await waitForSettle(200);
    // Add a model manually
    await card.locator('.btn-add-model').click();
    await card.locator('.model-row input').first().fill('new-model-x');
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(1000);

    // Navigate back to the task (re-opens TaskChat which picks up new config)
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await waitForSettle(3000); // wait for onMount + config store subscription

    // Task may appear "new" again (no persisted messages), click Start if needed
    const startBtn2 = page.locator('.btn-start-task');
    if (await startBtn2.isVisible().catch(() => false)) {
      await startBtn2.click({ timeout: 5000 }).catch(() => {});
      await waitForSettle(1000);
    }
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });

    // Model selector should now be visible with the new provider's model
    await expect(modelTrigger).toBeVisible({ timeout: 15000 });
    await modelTrigger.click();
    await waitForSettle();
    await expect(page.locator('.dropdown')).toBeVisible();
    await expect(page.locator('.provider-label')).toContainText('Newprovider.local:9999');
    await expect(page.locator('.model-option')).toContainText('new-model-x');
  });
});
