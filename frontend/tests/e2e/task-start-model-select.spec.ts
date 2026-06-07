import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';
import http from 'node:http';

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

const uniqueUrl = `http://task-select-${Date.now()}.local/v1`;

test.describe.serial('Provider/Model Selection at Task Start', () => {
  const testConfig = {
    providers: [
      { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: '' },
    ],
  };

  test.beforeEach(async ({ page }) => {
    await apiPutConfig({ ...testConfig });
    await waitForSettle(200);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('settings: clicking star sets per-provider default_model and persists on save', async ({ page }) => {
    // Set config fresh right before opening settings (avoids parallel test interference)
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: '' },
      ],
    });
    await waitForSettle(300);
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await waitForSettle(500); // extra settle after navigation

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Find our provider card by URL (resilient to parallel test interference)
    const ourCard = page.locator('.provider-card').filter({ has: page.locator('.url-input', { hasText: 'task-select' }) });
    await expect(ourCard).toBeVisible();

    const modelRows = ourCard.locator('.model-row');
    await expect(modelRows).toHaveCount(2);

    // Find the star button for model-beta (2nd model row)
    const betaRow = modelRows.nth(1); // model-beta is the 2nd model
    const starBtn = betaRow.locator('.btn-star');
    await expect(starBtn).toBeVisible();

    // Verify it's next to model-beta by checking input value
    const betaInput = betaRow.locator('input');
    await expect(betaInput).toHaveValue('model-beta');

    // Click star to set as default
    await starBtn.click();
    await waitForSettle(200);

    // Star should now be filled (active)
    await expect(starBtn).toHaveClass(/active/);

    // Save settings
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    // Verify per-provider default_model persisted (find by URL, not index)
    const config = await apiGetConfig();
    const ourProvider = config.providers.find((p: any) => p.url === uniqueUrl);
    expect(ourProvider).toBeDefined();
    expect(ourProvider.default_model).toBe('model-beta');
  });

  test('settings: toggling star off clears per-provider default_model', async ({ page }) => {
    // Set a default first
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: 'model-alpha' },
      ],
    });
    await waitForSettle(200);

    // Reload to pick up config
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Find our provider card by URL
    const ourCard = page.locator('.provider-card').filter({ has: page.locator('.url-input', { hasText: 'task-select' }) });
    await expect(ourCard).toBeVisible();

    // Find the star button for model-alpha (1st model row)
    const alphaRow = ourCard.locator('.model-row').nth(0);
    const starBtn = alphaRow.locator('.btn-star');
    await expect(starBtn).toHaveClass(/active/);

    // Click to toggle off
    await starBtn.click();
    await waitForSettle(200);

    // Star should no longer be active
    await expect(starBtn).not.toHaveClass(/active/);

    // Save
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(500);

    const config = await apiGetConfig();
    const ourProvider = config.providers.find((p: any) => p.url === uniqueUrl);
    expect(ourProvider).toBeDefined();
    expect(ourProvider.default_model).toBe('');
  });

  test('CreateTaskModal: model selector visible and pre-selects default', async ({ page }) => {
    // Set a default model on the provider
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: 'model-beta' },
      ],
    });
    await waitForSettle(200);

    // Reload to pick up config
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Open create task modal
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Model selector should be visible
    await expect(page.locator('.model-selector')).toBeVisible();

    // Default model should be pre-selected
    await expect(page.locator('.model-name')).toContainText('model-beta');
  });

  test('CreateTaskModal: button disabled when no model selected (no default)', async ({ page }) => {
    // No default model set on provider
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: '' },
      ],
    });
    await waitForSettle(200);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // With new behavior, first model is auto-selected as fallback
    // So the button should be enabled
    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // First model should be pre-selected as fallback
    await expect(page.locator('.model-name')).toContainText('model-alpha');

    // Fill in prompt
    await page.fill('textarea', 'test task');
    await waitForSettle(200);

    // Create button should be enabled (model is auto-selected)
    const createBtn = page.locator('.btn-primary');
    await expect(createBtn).toBeEnabled();
  });

  test('CreateTaskModal: created task has selected model', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: '' },
      ],
    });
    await waitForSettle(200);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectId = await getProjectId(page);

    await page.click('.add-task-btn');
    await page.waitForSelector('.modal', { timeout: 5000 });

    // Fill prompt and select model
    await page.fill('textarea', 'test task with model');
    await page.locator('.model-trigger').click();
    await waitForSettle(200);
    await page.locator('.model-option').filter({ hasText: 'model-beta' }).click();
    await waitForSettle(500);

    // Submit
    await page.click('button[type="submit"]');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Verify the task was created with the model
    const tasks = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      return res.json();
    }, { pid: projectId });

    const newTask = tasks.tasks[tasks.tasks.length - 1];
    expect(newTask.model_name).toBe('model-beta');
    expect(newTask.llama_server_url).toBe(uniqueUrl);
  });

  test('TaskChat: model selector in chat form after task starts', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test Provider', url: uniqueUrl, models: ['model-alpha', 'model-beta'], default_model: 'model-alpha' },
      ],
    });
    await waitForSettle(500);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `chat-form-test-${Date.now()}`);

    // Navigate to task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-detail-page', { timeout: 10000 });
    await waitForSettle(3000);

    // "Start Task" button should be visible
    const startBtn = page.locator('.btn-start-task');
    await expect(startBtn).toBeVisible({ timeout: 10000 });

    // With default model set, button should be enabled
    await expect(startBtn).toBeEnabled();

    // Click start - this triggers the executor
    await startBtn.click();
    await waitForSettle(1000);

    // ChatForm should appear with model selector
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.chat-form .model-selector')).toBeVisible({ timeout: 10000 });
  });

});
