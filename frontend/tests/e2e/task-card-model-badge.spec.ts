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

test.describe.serial('Task Card Model Badge', () => {
  test('shows model name on card when single provider is configured', async ({ page }) => {
    // Set up single provider config
    const taskTitle = `badge-test-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'solo-provider', display_name: 'Solo', url: 'http://solo.local/v1', models: ['solo-model'], default_model: 'solo-model' },
      ],
      default_provider: 'solo-provider',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(1);
    }).toPass({ timeout: 5000 });

    // Navigate to the app
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a task with the model set directly via API
    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt: title,
          model_name: 'solo-model',
          llama_server_url: 'http://solo.local/v1',
        }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    // Verify task has model set
    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('solo-model');
    }).toPass({ timeout: 5000 });

    // Reload board to pick up the task
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000); // Allow config store to hydrate

    // Find the specific card by title and check its model badge
    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible({ timeout: 10000 });
    await expect(modelBadge).toContainText('solo-model');

    // Should NOT show provider name (single provider)
    await expect(modelBadge).not.toContainText('Solo');
  });

  test('shows provider + model on card when multiple providers are configured', async ({ page }) => {
    // Set up multiple providers config
    const taskTitle = `multi-badge-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'provider-alpha', display_name: 'Alpha', url: 'http://alpha.local/v1', models: ['alpha-model'], default_model: 'alpha-model' },
        { name: 'provider-beta', display_name: 'Beta', url: 'http://beta.local/v1', models: ['beta-model'], default_model: 'beta-model' },
      ],
      default_provider: 'provider-alpha',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(2);
    }).toPass({ timeout: 5000 });

    // Navigate to the app
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a task with the model set directly
    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt: title,
          model_name: 'alpha-model',
          llama_server_url: 'http://alpha.local/v1',
        }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    // Verify task has model set
    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('alpha-model');
    }).toPass({ timeout: 5000 });

    // Reload board
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    // Find the specific card by title
    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible({ timeout: 10000 });
    await expect(modelBadge).toContainText('Alpha');
    await expect(modelBadge).toContainText('alpha-model');
  });

  test('does not crash when no model is set on task', async ({ page }) => {
    // Set up single provider config
    await apiPutConfig({
      providers: [
        { name: 'test-provider', display_name: 'Test', url: 'http://test.local/v1', models: ['test-model'], default_model: 'test-model' },
      ],
      default_provider: 'test-provider',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);

    // Navigate to the app
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a task WITHOUT setting model
    const projectId = await getProjectId(page);
    await createTaskViaApi(page, projectId, `no-model-${Date.now()}`);

    // Reload board
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Board should still be visible (no crash)
    await expect(page.locator('.board')).toBeVisible();
  });

  test('card_model_badge=always shows provider+model even with single provider', async ({ page }) => {
    const taskTitle = `always-badge-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'only-provider', display_name: 'Only', url: 'http://only.local/v1', models: ['only-model'], default_model: 'only-model' },
      ],
      default_provider: 'only-provider',
      card_model_badge: 'always',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.card_model_badge).toBe('always');
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title, model_name: 'only-model', llama_server_url: 'http://only.local/v1' }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('only-model');
    }).toPass({ timeout: 5000 });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible({ timeout: 10000 });
    // With "always", should show provider+model even with single provider
    const badgeText = await modelBadge.textContent();
    expect(badgeText).toContain('Only');
    expect(badgeText).toContain('only-model');
  });

  test('card_model_badge=model shows model name only', async ({ page }) => {
    const taskTitle = `model-badge-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'provider-a', display_name: 'ProviderA', url: 'http://a.local/v1', models: ['model-a'], default_model: 'model-a' },
        { name: 'provider-b', display_name: 'ProviderB', url: 'http://b.local/v1', models: ['model-b'], default_model: 'model-b' },
      ],
      default_provider: 'provider-a',
      card_model_badge: 'model',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.card_model_badge).toBe('model');
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title, model_name: 'model-a', llama_server_url: 'http://a.local/v1' }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('model-a');
    }).toPass({ timeout: 5000 });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible({ timeout: 10000 });
    // With "model", should show model name only (no provider)
    await expect(modelBadge).toContainText('model-a');
    await expect(modelBadge).not.toContainText('ProviderA');
  });

  test('card_model_badge=none hides the model badge', async ({ page }) => {
    const taskTitle = `none-badge-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'hide-provider', display_name: 'Hide', url: 'http://hide.local/v1', models: ['hide-model'], default_model: 'hide-model' },
      ],
      default_provider: 'hide-provider',
      card_model_badge: 'none',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.card_model_badge).toBe('none');
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    await page.evaluate(async ({ pid, title }) => {
      await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title, model_name: 'hide-model', llama_server_url: 'http://hide.local/v1' }),
      });
    }, { pid: projectId, title: taskTitle });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();
    // With "none", model badge should not be visible
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).not.toBeVisible();
  });

  test('model badge shows default model and picker works for tasks without model set (Backlog)', async ({ page }) => {
    const taskTitle = `model-picker-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'prov-x', display_name: 'ServerX', url: 'http://x.local/v1', models: ['model-x1', 'model-x2'], default_model: 'model-x1' },
        { name: 'prov-y', display_name: 'ServerY', url: 'http://y.local/v1', models: ['model-y1'], default_model: 'model-y1' },
      ],
      default_provider: 'prov-x',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(2);
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    // Create a task WITHOUT setting model_name (simulates a fresh Backlog task)
    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title }), // no model_name
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    // Verify task has no model set
    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('');
    }).toPass({ timeout: 5000 });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();

    // Badge should show the default model even though task has no model set
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible({ timeout: 10000 });
    await expect(modelBadge).toContainText('model-x1'); // default model

    // Click to open picker (works in Backlog)
    await modelBadge.click();
    await waitForSettle(200);
    await expect(page.locator('.portal-card-model-picker')).toBeVisible();

    // Select a different model
    await page.locator('.picker-model-option').filter({ hasText: 'model-y1' }).click();
    await waitForSettle(500);
    await expect(page.locator('.portal-card-model-picker')).not.toBeVisible();

    // Verify task model was set
    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('model-y1');
    }).toPass({ timeout: 5000 });
  });

  test('model badge is read-only (not clickable) in In Progress column', async ({ page }) => {
    const taskTitle = `readonly-badge-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'prov-r', display_name: 'ServerR', url: 'http://r.local/v1', models: ['model-r1', 'model-r2'], default_model: 'model-r1' },
      ],
      default_provider: 'prov-r',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    // Create task and move it to in_progress
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title, model_name: 'model-r1', llama_server_url: 'http://r.local/v1' }),
      });
      const task = await res.json();
      // Move to in_progress
      await fetch(`/api/projects/${pid}/tasks/${task.id}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'in_progress' }),
      });
      return task.id;
    }, { pid: projectId, title: taskTitle });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    // Find the card in the In Progress column
    const inProgressColumn = page.locator('.column').filter({ has: page.locator('.column-header h3:has-text("In Progress")') });
    const taskCard = inProgressColumn.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();

    // Badge should be visible but NOT a button (no chevron)
    const modelBadge = taskCard.locator('.model-badge');
    await expect(modelBadge).toBeVisible();
    await expect(modelBadge).toContainText('model-r1');
    // Chevron should NOT be present (read-only)
    await expect(taskCard.locator('.badge-chevron')).not.toBeVisible();

    // Clicking should NOT open the picker
    await modelBadge.click();
    await waitForSettle(300);
    await expect(page.locator('.portal-card-model-picker')).not.toBeVisible();
  });

  test('clicking model badge opens picker and changing model updates the task', async ({ page }) => {
    const taskTitle = `picker-test-${Date.now()}`;
    await apiPutConfig({
      providers: [
        { name: 'prov-a', display_name: 'ServerA', url: 'http://a.local/v1', models: ['model-alpha', 'model-beta'], default_model: 'model-alpha' },
        { name: 'prov-b', display_name: 'ServerB', url: 'http://b.local/v1', models: ['model-gamma'], default_model: 'model-gamma' },
      ],
      default_provider: 'prov-a',
      card_model_badge: 'auto',
    });
    await waitForSettle(500);
    await expect(async () => {
      const config = await apiGetConfig();
      expect(config.providers).toHaveLength(2);
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });

    const projectId = await getProjectId(page);
    const taskId = await page.evaluate(async ({ pid, title }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: title, model_name: 'model-alpha', llama_server_url: 'http://a.local/v1' }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId, title: taskTitle });

    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('model-alpha');
    }).toPass({ timeout: 5000 });

    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await waitForSettle(1000);

    const taskCard = page.locator('.card').filter({ has: page.locator('.card-title:has-text("' + taskTitle + '")') });
    await expect(taskCard).toBeVisible();

    // Click the model badge to open the picker
    const modelBadge = taskCard.locator('.model-badge');
    await modelBadge.click();
    await waitForSettle(200);

    // Dropdown should be visible
    await expect(page.locator('.portal-card-model-picker')).toBeVisible();

    // Should list models from both providers
    await expect(page.locator('.picker-provider-header')).toHaveCount(2);
    await expect(page.locator('.picker-model-option')).toHaveCount(3);

    // Select a different model
    await page.locator('.picker-model-option').filter({ hasText: 'model-beta' }).click();
    await waitForSettle(500);

    // Dropdown should close
    await expect(page.locator('.portal-card-model-picker')).not.toBeVisible();

    // Verify the task model was updated
    await expect(async () => {
      const taskData = await page.evaluate(async ({ pid, tid }) => {
        const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(taskData.model_name).toBe('model-beta');
    }).toPass({ timeout: 5000 });

    // Badge text should update to show new model
    await expect(modelBadge).toContainText('model-beta');
  });
});
