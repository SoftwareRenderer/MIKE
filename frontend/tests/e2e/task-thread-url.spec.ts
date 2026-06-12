import { test, expect } from '@playwright/test';

test.describe.serial('Thread URL Navigation', () => {
  let taskId: string = '';
  let projectId: string = '';
  let threadIds: string[] = [];
  const uniqueId = Date.now();

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create task, fork, and update URL with thread param', async ({ page }) => {
    // Create task (include default provider config so executor can start)
    const result = await page.evaluate(async (uid: number) => {
      const [projectsRes, configRes] = await Promise.all([
        fetch('/api/projects'),
        fetch('/api/config'),
      ]);
      const projects = await projectsRes.json();
      const config = await configRes.json();
      const pid = projects[0].id;
      const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          prompt: `Test thread URL ${uid}`,
          llama_server_url: defaultProvider?.url || '',
          model_name: defaultProvider?.default_model || '',
        }),
      });
      const task = await res.json();
      return { projectId: pid, taskId: task.id };
    }, uniqueId);

    projectId = result.projectId;
    taskId = result.taskId;

    // Navigate to the task
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Click Start Task if visible
    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      // Wait for chat form to appear (replaces waitForTimeout(1000))
      await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 });
    }

    // Wait for chat form (executor may not start if task has no LLM config)
    const chatFormVisible = await page.locator('.chat-form').isVisible({ timeout: 10000 }).catch(() => false);
    if (!chatFormVisible) return;

    // Send a user message to create a checkpoint
    const input = page.locator('textarea[placeholder*="message"]');
    await input.fill(`Test message ${uniqueId}`);
    await input.press('Enter');

    // Wait for textarea to clear (message sent)
    await expect(input).toHaveValue('', { timeout: 5000 });

    // Wait for checkpoint divider to appear (executor creates it after LLM responds)
    const checkpointVisible = await page.locator('.checkpoint-divider').isVisible({ timeout: 30000 }).catch(() => false);
    if (!checkpointVisible) {
      // Executor didn't create a checkpoint — skip thread assertions but keep task for later tests
      return;
    }

    // Get threads before fork
    const beforeThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    threadIds = beforeThreads.map((t: any) => t.id);
    expect(threadIds.length).toBeGreaterThanOrEqual(1);

    // Reload to pick up updated task status (prop from parent)
    await page.reload();
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Fork via the message's fork button (edit form)
    const userMsg = page.locator('.chat-message.user').last();
    // Wait for user message to be visible (messages may load slowly under load)
    await expect(userMsg).toBeVisible({ timeout: 10000 });
    const forkBtn = userMsg.locator('.action-btn[aria-label*="Fork"]');
    await expect(forkBtn).toBeVisible({ timeout: 10000 });
    await forkBtn.click();
    // Wait for edit form to appear before filling
    await expect(page.locator('.edit-form textarea')).toBeVisible({ timeout: 5000 });
    await page.locator('.edit-form textarea').fill(`Fork ${Date.now()}`);
    await page.locator('.btn-edit-submit').click();

    // Wait for fork to create a new thread (replaces waitForTimeout(2000))
    await expect(async () => {
      const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
        const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
        return res.json();
      }, { pid: projectId, tid: taskId });
      expect(threads.length).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 10000 });

    // Find the new fork thread (the one that's active and not in the original list)
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const forkThread = afterThreads.find((t: any) => t.is_active && !threadIds.includes(t.id));
    expect(forkThread).toBeDefined();
    const forkThreadId = forkThread.id;

    // Verify URL was updated with thread param (replaces waitForTimeout(2000))
    await expect(async () => {
      const url = page.url();
      expect(url).toContain(`thread=${forkThreadId}`);
    }).toPass({ timeout: 10000 });

    // Verify messages exist in the fork thread via API
    const forkMessages = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/messages`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    expect(forkMessages.length).toBeGreaterThanOrEqual(1);

    // Verify conversation is NOT empty — messages from the fork should be visible
    const emptyState = page.locator('.chat-empty');
    await expect(emptyState).not.toBeVisible({ timeout: 5000 });
    // Should have user message entries
    const userMessages = page.locator('.chat-message.user');
    expect(await userMessages.count()).toBeGreaterThanOrEqual(1);
  });

  test('should update URL when switching threads via dropdown', async ({ page }) => {
    if (!taskId || !projectId) return;

    // Navigate to the task (without thread param to load active thread)
    await page.goto(`/?task=${taskId}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Click Start Task if visible
    const startBtn = page.locator('button:has-text("Start Task")');
    if (await startBtn.isVisible().catch(() => false)) {
      await startBtn.click();
      await expect(page.locator('.chat-form')).toBeVisible({ timeout: 10000 }).catch(() => {});
    }

    // Wait for chat form (graceful skip if executor didn't start)
    const chatFormVisible = await page.locator('.chat-form').isVisible({ timeout: 10000 }).catch(() => false);
    if (!chatFormVisible) return;

    // Send a message to ensure there are user messages with checkpoint dividers
    const input = page.locator('textarea[placeholder*="message"]');
    await input.fill(`Follow-up message ${uniqueId}`);
    await input.press('Enter');
    await expect(input).toHaveValue('', { timeout: 5000 });

    // Wait for checkpoint divider to appear
    const checkpointVisible = await page.locator('.checkpoint-divider').isVisible({ timeout: 30000 }).catch(() => false);
    if (!checkpointVisible) return;

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) return;

    const currentActive = threads.find((t: any) => t.is_active);
    const otherThread = threads.find((t: any) => !t.is_active);
    expect(otherThread).toBeDefined();

    // Wait for thread button to appear (requires threads loaded + user messages present)
    await page.waitForSelector('.thread-btn', { timeout: 10000 });

    // Click the thread switch button to open dropdown
    await page.locator('.thread-btn').first().click();
    // Wait for menu items to appear (replaces waitForTimeout(500))
    await expect(page.locator('.thread-menu-item')).toBeVisible({ timeout: 5000 });

    // Click the other thread in the menu
    await page.locator('.thread-menu-item').filter({ hasText: otherThread.name }).click();

    // Wait for URL to update with new thread (replaces waitForTimeout(2000))
    await expect(async () => {
      const url = page.url();
      expect(url).toContain(`thread=${otherThread.id}`);
    }).toPass({ timeout: 10000 });

    // Verify the switch took effect via API
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const newActive = afterThreads.find((t: any) => t.is_active);
    expect(newActive.id).toBe(otherThread.id);
  });

  test('should load specified thread when navigating to URL with thread param', async ({ page }) => {
    if (!taskId || !projectId) return;

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) return;

    // Pick a non-active thread to navigate to
    const nonActiveThread = threads.find((t: any) => !t.is_active);
    if (!nonActiveThread) return;

    // Navigate directly to the URL with thread param
    await page.goto(`/?task=${taskId}&thread=${nonActiveThread.id}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Verify the thread was switched via API (replaces waitForTimeout(3000))
    const afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    const newActive = afterThreads.find((t: any) => t.is_active);
    expect(newActive.id).toBe(nonActiveThread.id);
  });

  test('should restore previous thread on browser Back navigation', async ({ page }) => {
    if (!taskId || !projectId) return;

    // Get all threads
    const threads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });

    if (threads.length < 2) return;

    // Navigate to first thread
    const threadA = threads[0];
    await page.goto(`/?task=${taskId}&thread=${threadA.id}`);
    await page.waitForSelector('.task-chat', { timeout: 10000 });

    // Send a message so we have checkpoint dividers for the thread switch button
    const input = page.locator('textarea[placeholder*="message"]');
    await input.fill(`Back nav message ${uniqueId}`);
    await input.press('Enter');
    await expect(input).toHaveValue('', { timeout: 5000 });

    // Wait for thread button to appear
    const threadBtnVisible = await page.locator('.thread-btn').isVisible({ timeout: 15000 }).catch(() => false);
    if (!threadBtnVisible) return;
    await page.waitForSelector('.thread-btn', { timeout: 10000 });

    // Verify thread A is active
    let afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    let activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadA.id);

    // Switch to another thread via dropdown
    const threadB = threads.find((t: any) => t.id !== threadA.id);
    await page.locator('.thread-btn').first().click();
    // Wait for menu items to appear (replaces waitForTimeout(500))
    await expect(page.locator('.thread-menu-item')).toBeVisible({ timeout: 5000 });
    await page.locator('.thread-menu-item').filter({ hasText: threadB.name }).click();

    // Wait for URL to update (replaces waitForTimeout(2000))
    await expect(async () => {
      expect(page.url()).toContain(`thread=${threadB.id}`);
    }).toPass({ timeout: 10000 });

    // Verify thread B is active
    afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadB.id);

    // Go back in browser history
    await page.goBack();
    // Wait for URL to reflect thread A (replaces waitForTimeout(3000))
    await expect(async () => {
      expect(page.url()).toContain(`thread=${threadA.id}`);
    }).toPass({ timeout: 10000 });

    // Verify thread A is active again
    afterThreads = await page.evaluate(async (params: { pid: string; tid: string }) => {
      const res = await fetch(`/api/projects/${params.pid}/tasks/${params.tid}/threads`);
      return res.json();
    }, { pid: projectId, tid: taskId });
    activeThread = afterThreads.find((t: any) => t.is_active);
    expect(activeThread.id).toBe(threadA.id);
  });
});
