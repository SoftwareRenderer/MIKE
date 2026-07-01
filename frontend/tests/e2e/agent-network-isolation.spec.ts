import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.serial('Agent + Network Isolation', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should block task start when agent provider + network isolation enabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Enable sandbox + network isolation on the project
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: true },
    });

    // Verify network isolation is enabled
    const projectData = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(projectData.sandbox_network_isolation).toBe(true);

    // Create a task with a Pi agent provider (pi://agent sentinel URL)
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `agent-network-test-${Date.now()}`,
        llama_server_url: 'pi://agent',
        model_name: null,
      },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;
    expect(taskId).toBeTruthy();

    // Try to start the task — should fail with agent error
    const startResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${taskId}/start`,
      { method: 'POST' }
    );
    expect(startResult.ok).toBe(false);
    expect(startResult.status).toBe(500);
    expect(startResult.data?.error).toContain('agent requires network to be enabled');

    // Verify task is still in backlog (not started)
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskData.status).toBe('backlog');
  });

  test('should allow task start when agent provider + network isolation disabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Ensure network isolation is disabled
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false, sandbox_network_isolation: false },
    });

    // Create a task with a Pi agent provider
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `agent-allowed-test-${Date.now()}`,
        llama_server_url: 'pi://agent',
        model_name: null,
      },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;

    // Start the task — should succeed (no network isolation)
    const startResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${taskId}/start`,
      { method: 'POST' }
    );
    expect(startResult.ok).toBe(true);

    // Verify task is in_progress
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskData.status).toBe('in_progress');
  });

  test('should allow task start when LLM provider + network isolation enabled', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Enable network isolation
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: true },
    });

    // Get the default LLM provider
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.ok ? res.json() : null;
    });
    const defaultProvider = config?.providers?.find(
      (p: any) => p.name === config?.default_provider
    );

    // Create a task with an LLM provider (not an agent)
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: {
        prompt: `llm-network-test-${Date.now()}`,
        llama_server_url: defaultProvider?.url || '',
        model_name: defaultProvider?.default_model || '',
      },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;

    // Start the task — should succeed (LLM provider, not an agent)
    const startResult = await csrfFetch(
      page,
      `/api/projects/${projectId}/tasks/${taskId}/start`,
      { method: 'POST' }
    );
    expect(startResult.ok).toBe(true);

    // Verify task is in_progress
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.ok ? res.json() : null;
    }, { pid: projectId, tid: taskId });
    expect(taskData.status).toBe('in_progress');
  });
});
