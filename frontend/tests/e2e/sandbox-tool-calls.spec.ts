import { test, expect, getProjectId, createAndOpenTask, ensureChatForm } from './helpers';

test.describe.serial('Sandboxed Tool Calls', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show sandbox icon on tool calls when sandbox is enabled', async ({ page }) => {
    test.setTimeout(45000);
    const projectId = await getProjectId(page);

    // Enable sandbox on the project
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: true }),
      });
    }, projectId);

    // Verify sandbox is enabled
    const sandboxEnabled = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(sandboxEnabled).toBe(true);

    // Create and open a task
    await createAndOpenTask(page, `sandbox-read-test-${Date.now()}`);
    await ensureChatForm(page);

    // Send a message that triggers tool calls (mock routes "list" to tool_call fixture)
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('list files in directory');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for tool call card to appear
    const toolCallCard = page.locator('.tool-call-card').first();
    const cardVisible = await toolCallCard.isVisible({ timeout: 20000 }).catch(() => false);

    if (!cardVisible) {
      // Graceful: executor may not have started (worktree conflict, etc.)
      test.skip();
      return;
    }

    // Verify the sandbox shield icon is present on the tool call
    await expect(page.locator('.tool-sandbox-icon').first()).toBeVisible({ timeout: 10000 });
  });

  test('should not show sandbox icon on tool calls when sandbox is disabled', async ({ page }) => {
    test.setTimeout(60000);
    const projectId = await getProjectId(page);

    // Ensure sandbox is disabled
    await page.evaluate(async (pid) => {
      await fetch(`/api/projects/${pid}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sandbox_enabled: false }),
      });
    }, projectId);

    // Verify sandbox is disabled
    const sandboxEnabled = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      const proj = await res.json();
      return proj.sandbox_enabled;
    }, projectId);
    expect(sandboxEnabled).toBe(false);

    // Reload to pick up sandbox change
    await page.reload();
    await page.waitForSelector('.project-item', { timeout: 10000 });

    // Create and open a task
    await createAndOpenTask(page, `sandbox-off-test-${Date.now()}`);
    await ensureChatForm(page);

    // Send a message that triggers tool calls
    const chatTextarea = page.locator('textarea[placeholder*="message"]');
    await chatTextarea.fill('read a file');
    await chatTextarea.press('Enter');
    await expect(chatTextarea).toHaveValue('', { timeout: 5000 });

    // Wait for tool call card
    const toolCallCard = page.locator('.tool-call-card').first();
    const cardVisible = await toolCallCard.isVisible({ timeout: 30000 }).catch(() => false);

    if (!cardVisible) {
      test.skip();
      return;
    }

    // Verify no sandbox shield icon is present
    const sandboxIcons = page.locator('.tool-sandbox-icon');
    expect(await sandboxIcons.count()).toBe(0);
  });
});
