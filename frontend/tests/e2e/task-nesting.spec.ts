import { test, expect } from './helpers';

test.describe.serial('Task Nesting', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should show + button on backlog cards for creating subtasks', async ({ page }) => {
    // Wait for at least one card in the backlog column
    const backlogColumn = page.locator('.column').first();
    const cards = backlogColumn.locator('.card');

    // If no cards exist, create one first
    const cardCount = await cards.count();
    if (cardCount === 0) {
      await page.click('.add-task-btn');
      await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
      await page.fill('textarea', `parent-task-${Date.now()}`);
      await page.click('button[type="submit"]');
      await expect(page.locator('.modal')).not.toBeVisible();
      await page.waitForSelector('.card', { timeout: 5000 });
    }

    // Hover over the first card to reveal action buttons
    const firstCard = page.locator('.column:first-child .card').first();
    await firstCard.hover();

    // The child button (+) should appear on root tasks in backlog
    // It has aria-label="Create subtask"
    const childBtn = firstCard.locator('button[aria-label="Create subtask"]');
    await expect(childBtn).toBeVisible({ timeout: 5000 });
  });

  test('should create a child task via the + button', async ({ page }) => {
    const parentId = `parent-${Date.now()}`;

    // Create a parent task first
    await page.click('.add-task-btn');
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await page.fill('textarea', parentId);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();
    await page.waitForSelector('.card', { timeout: 5000 });

    // Find the parent card and hover to reveal the + button
    const parentCard = page.locator('.column:first-child .card').filter({ hasText: parentId });
    await expect(parentCard).toBeVisible({ timeout: 5000 });
    await parentCard.hover();

    // Click the + button to create a subtask
    const childBtn = parentCard.locator('button[aria-label="Create subtask"]');
    await childBtn.click();

    // Modal should open with "Create Subtask" title
    await expect(page.locator('.modal')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('#create-task-title')).toContainText('Create Subtask');

    // Should show parent label
    const parentLabel = page.locator('.parent-label');
    await expect(parentLabel).toBeVisible();
    await expect(parentLabel).toContainText(parentId);

    // Fill in and submit the child task
    const childPrompt = `child-task-${Date.now()}`;
    await page.fill('textarea', childPrompt);
    await page.click('button[type="submit"]');
    await expect(page.locator('.modal')).not.toBeVisible();

    // Wait for the new card to appear
    await page.waitForTimeout(500);

    // Verify the child task exists in the API with correct parent_task_id
    const projectId = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      return projects[0]?.id || '';
    });

    const childData = await page.evaluate(async ({ pid, prompt }) => {
      const res = await fetch(`/api/projects/${pid}/board`);
      const board = await res.json();
      return board.tasks.find((t: any) => t.prompt === prompt) || null;
    }, { pid: projectId, prompt: childPrompt });

    expect(childData).not.toBeNull();
    expect(childData.parent_task_id).toBeTruthy();
  });

  test('should render child tasks indented under parent', async ({ page }) => {
    // Create parent via API
    const projectId = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      return projects[0]?.id || '';
    });

    const parentId = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `indent-parent-${Date.now()}` }),
      });
      const task = await res.json();
      return task.id;
    }, { pid: projectId });

    // Create child via API
    await page.evaluate(async ({ pid, parentId }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `indent-child-${Date.now()}`, parent_task_id: parentId }),
      });
      return res.json();
    }, { pid: projectId, parentId });

    // Refresh the board view
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Find the backlog column
    const backlogColumn = page.locator('.column').first();

    // Find cards with the parent and child prompts
    const allCards = backlogColumn.locator('.card');
    const cardCount = await allCards.count();
    expect(cardCount).toBeGreaterThan(0);

    // Check that at least one card has the indented class (child task)
    const indentedCards = backlogColumn.locator('.card.indented');
    const indentedCount = await indentedCards.count();
    expect(indentedCount).toBeGreaterThanOrEqual(1);
  });

  test('should reject child task creation for non-backlog parent', async ({ page }) => {
    const projectId = await page.evaluate(async () => {
      const res = await fetch('/api/projects');
      const projects = await res.json();
      return projects[0]?.id || '';
    });

    // Create a task and move it to 'done' (non-backlog)
    const taskId = await page.evaluate(async ({ pid }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `non-backlog-parent-${Date.now()}` }),
      });
      const task = await res.json();
      // Move task to 'done' so it's no longer in backlog
      const moveRes = await fetch(`/api/projects/${pid}/tasks/${task.id}/move`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ status: 'done' }),
      });
      await moveRes.json();
      return task.id;
    }, { pid: projectId });

    // Try to create a child with this non-backlog parent via API
    const errorResult = await page.evaluate(async ({ pid, parentId }) => {
      const res = await fetch(`/api/projects/${pid}/tasks`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: `should-fail-${Date.now()}`, parent_task_id: parentId }),
      });
      if (!res.ok) {
        const err = await res.json();
        return err.error || 'Unknown error';
      }
      return null; // No error means it succeeded (unexpected)
    }, { pid: projectId, parentId: taskId });

    expect(errorResult).toBeTruthy();
    expect(String(errorResult)).toContain('backlog');
  });
});
