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

test.describe.serial('Model Selector', () => {
  test('should show model selector in chat form when providers are configured', async ({ page }) => {
    await apiPutConfig({
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, 'test task');
    await page.locator('.card').last().click();
    await page.waitForSelector('.task-detail-page', { timeout: 5000 });
    await waitForSettle(1000);
    await expect(page.locator('.model-selector')).toBeVisible();
    await expect(page.locator('.model-trigger')).toBeVisible();
  });

  test('should open dropdown when model trigger is clicked', async ({ page }) => {
    await apiPutConfig({
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, 'test task');
    await page.locator('.card').last().click();
    await page.waitForSelector('.task-detail-page', { timeout: 5000 });
    await waitForSettle(1000);
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await expect(page.locator('.dropdown')).toBeVisible();
    await expect(page.locator('.model-option')).toHaveCount(2);
    await expect(page.locator('.provider-label')).toContainText('Test Provider');
  });

  test('should update task when a model is selected', async ({ page }) => {
    await apiPutConfig({
      providers: [{ name: 'test-provider', display_name: 'Test Provider', url: 'http://localhost:8080/v1', models: ['model-alpha', 'model-beta'] }],
      default_provider: 'test-provider',
    });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, 'test task');
    await page.locator('.card').last().click();
    await page.waitForSelector('.task-detail-page', { timeout: 5000 });
    await waitForSettle(1000);
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await page.locator('.model-option').filter({ hasText: 'model-beta' }).click();
    await waitForSettle(1000);
    await expect(page.locator('.model-name')).toContainText('model-beta');
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`${window.location.origin}/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.model_name).toBe('model-beta');
    expect(taskData.llama_server_url).toBe('http://localhost:8080/v1');
  });

  test('should not show model selector when no providers are configured', async ({ page }) => {
    await apiPutConfig({ providers: [], default_provider: '' });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, 'test task');
    await page.locator('.card').last().click();
    await page.waitForSelector('.task-detail-page', { timeout: 5000 });
    await waitForSettle(1000);
    await expect(page.locator('.model-selector')).not.toBeVisible();
  });

  test('should show models grouped by provider in dropdown', async ({ page }) => {
    await apiPutConfig({
      providers: [
        { name: 'provider-a', display_name: 'Provider A', url: 'http://a.example/v1', models: ['model-a1', 'model-a2'] },
        { name: 'provider-b', display_name: 'Provider B', url: 'http://b.example/v1', models: ['model-b1'] },
      ],
      default_provider: 'provider-a',
    });
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, 'test task');
    await page.locator('.card').last().click();
    await page.waitForSelector('.task-detail-page', { timeout: 5000 });
    await waitForSettle(1000);
    await page.locator('.model-trigger').click();
    await waitForSettle();
    await expect(page.locator('.provider-label')).toHaveCount(2);
    await expect(page.locator('.provider-label').first()).toContainText('Provider A');
    await expect(page.locator('.provider-label').last()).toContainText('Provider B');
    await expect(page.locator('.model-option')).toHaveCount(3);
  });
});

test.describe.serial('Config Providers', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should add a provider via settings modal', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    await page.locator('.provider-card input').first().fill('my-provider');
    await page.locator('.provider-card input').nth(1).fill('My Provider');
    await page.locator('.provider-card input').nth(2).fill('http://example.com/v1');
    await page.getByRole('button', { name: 'Save' }).click();
    await waitForSettle(1000);
    const config = await apiGetConfig();
    expect(config.providers).toBeDefined();
    expect(Array.isArray(config.providers)).toBe(true);
    const myProvider = config.providers.find((p: any) => p.name === 'my-provider');
    expect(myProvider).toBeDefined();
    expect(myProvider.display_name).toBe('My Provider');
    expect(myProvider.url).toBe('http://example.com/v1');
  });

  test('should have Fetch Models button in provider card', async ({ page }) => {
    await page.getByTitle('Settings').click();
    await page.waitForSelector('.modal', { timeout: 5000 });
    await page.getByText('+ Add Provider').click();
    await waitForSettle();
    await page.locator('.provider-card input').nth(2).fill('http://example.com/v1');
    const fetchBtn = page.locator('.btn-fetch');
    await expect(fetchBtn).toBeVisible();
    await expect(fetchBtn).toBeEnabled();
    await expect(fetchBtn).toContainText('Fetch Models');
  });
});
