import { test, expect } from '@playwright/test';
import { getProjectId, waitForChatForm } from './helpers';

test.describe.serial('Plan Mode Subtasks', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item, .card', { timeout: 10000 });
  });

  test('should create subtasks via LLM in plan mode', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a plan-mode task via API
    const taskId = await page.evaluate(async ({ pid, prompt }) => {
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

      const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt,
          plan_mode: true,
          auto_commit: false,
          llama_server_url: defaultProvider?.url || '',
          model_name: defaultProvider?.default_model || '',
        }),
      });
      const task = await taskRes.json();
      return task.id;
    }, { pid: projectId, prompt: `plan-mode-test-${Date.now()} decompose this into subtasks` });

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat form and click start
    await waitForChatForm(page);

    // Wait for the LLM to finish (send button reappears)
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 60000 });

    // Task should be in review status (plan created, awaiting approval)
    const taskStatus = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      const task = await res.json();
      return { status: task.status, statusState: task.status_state };
    }, { pid: projectId, tid: taskId });

    expect(taskStatus.status).toBe('review');

    // Check for subtasks via API
    const subtaskCount = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/dependents`);
      const dependents = await res.json();
      return dependents.length;
    }, { pid: projectId, tid: taskId });

    expect(subtaskCount).toBeGreaterThan(0);
  });

  test('should create nested subtasks in plan mode', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a plan-mode task via API
    const taskId = await page.evaluate(async ({ pid, prompt }) => {
      const configRes = await fetch('/api/config');
      const config = await configRes.json();
      const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

      const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt,
          plan_mode: true,
          auto_commit: false,
          llama_server_url: defaultProvider?.url || '',
          model_name: defaultProvider?.default_model || '',
        }),
      });
      const task = await taskRes.json();
      return task.id;
    }, { pid: projectId, prompt: `plan-mode-nested-${Date.now()} create nested subtasks with deep hierarchy` });

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Wait for chat form and click start
    await waitForChatForm(page);

    // Wait for the LLM to finish
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 60000 });

    // Check that subtasks were created
    const subtasks = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}/dependents`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    expect(subtasks.length).toBeGreaterThan(0);
  });
});
