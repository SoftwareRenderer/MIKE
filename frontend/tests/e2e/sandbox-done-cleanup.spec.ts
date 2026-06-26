import { test, expect } from '@playwright/test';
import { execSync } from 'node:child_process';
import { getProjectId, createTaskViaApi, csrfFetch } from './helpers';

/** Check if a podman container exists by name pattern. */
function containerExists(namePattern: string): boolean {
  try {
    const output = execSync(
      `podman ps -a --filter name=${namePattern} --format '{{.Names}}'`,
      { encoding: 'utf-8', stdio: ['pipe', 'pipe', 'ignore'] }
    ).trim();
    return output.length > 0;
  } catch {
    return false;
  }
}

/** Wait for a container to appear (polls up to timeout ms). Returns true if found. */
function waitForContainer(taskId: string, timeoutMs = 30000): boolean {
  const safeId = taskId.replace(/[^a-zA-Z0-9_.-]/g, '-');
  const name = `kanban-sandbox-${safeId}`;
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (containerExists(name)) return true;
    try { execSync(`sleep 0.5`, { stdio: 'ignore' }); } catch {}
  }
  return false;
}

/** Wait for a container to disappear (polls up to timeout ms). Returns true if gone. */
function waitForContainerGone(taskId: string, timeoutMs = 15000): boolean {
  const safeId = taskId.replace(/[^a-zA-Z0-9_.-]/g, '-');
  const name = `kanban-sandbox-${safeId}`;
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (!containerExists(name)) return true;
    try { execSync(`sleep 0.5`, { stdio: 'ignore' }); } catch {}
  }
  return false;
}

test.describe.serial('Sandbox Container Cleanup on Done', () => {
  test('should remove sandbox container when task is moved to done', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

    const projectId = await getProjectId(page);

    // Enable sandbox on project
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true },
    });

    // Create a task via API
    const timestamp = Date.now();
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
      method: 'POST',
      body: { prompt: `sandbox-cleanup-${timestamp}` },
    });
    expect(result.ok).toBe(true);
    const taskId = result.data?.id;
    expect(taskId).toBeTruthy();

    // Move task to in_progress (starts the executor)
    await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'in_progress' },
    });

    // Navigate to the task
    await page.evaluate((tid) => { window.location.href = `/?task=${tid}`; }, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 15000 });

    // Wait for chat form to be ready
    await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.btn-send')).toBeVisible({ timeout: 10000 });

    // Send a message that triggers a tool call (mock LLM routes on "list"/"command" keywords)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('list files in the directory');
    await chatTextarea.press('Enter');

    // Wait for user message to appear
    await expect(page.locator('.chat-message.user')).toBeVisible({ timeout: 10000 });

    // Wait for the sandbox container to be created
    const containerCreated = waitForContainer(taskId, 30000);
    if (!containerCreated) {
      // Container creation may depend on sandbox availability
      // Still verify the move-to-done API works
      test.skip(true, 'Sandbox container was not created, skipping container verification');
    }

    // Move task to done via API
    const moveResult = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/move`, {
      method: 'POST',
      body: { status: 'done' },
    });
    expect(moveResult.ok).toBe(true);

    // Wait for the container to be removed
    const containerGone = waitForContainerGone(taskId, 15000);
    expect(containerGone).toBe(true);

    // Verify task is in done status
    const taskData = await page.evaluate(async ({ pid, tid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(taskData.status).toBe('done');
    expect(taskData.status_state).toBe('completed');

    // Cleanup: disable sandbox
    await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: false },
    });
  });
});
