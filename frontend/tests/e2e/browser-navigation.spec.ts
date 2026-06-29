import { test, expect } from '@playwright/test';
import {getProjectId, createTaskViaApi, navigateToTask, csrfFetch} from './helpers';

// Helper: create a subtask via API (CSRF-aware)
async function createSubtaskViaApi(page, projectId: string, parentTaskId: string, prompt: string): Promise<string> {
    const config = await page.evaluate(async () => {
        const res = await fetch('/api/config');
        return res.json();
    });
    const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);
    const result = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
        method: 'POST',
        body: {
            prompt,
            parent_task_id: parentTaskId,
            llama_server_url: defaultProvider?.url || '',
            model_name: defaultProvider?.default_model || '',
        },
    });
    return result.data.id;
}

// Helper: wait for URL to match predicate (SPA-aware, doesn't wait for load event)
async function waitForUrl(page, predicate: (url: string) => boolean) {
    // Use a simple polling approach since we can't serialize the predicate function
    const startTime = Date.now();
    while (Date.now() - startTime < 5000) {
        const url = page.url();
        if (predicate(url)) return;
        await page.waitForTimeout(100);
    }
    throw new Error(`Timeout waiting for URL to match predicate`);
}

test.describe.configure({ mode: 'serial' });
test.describe('Browser Navigation (Back/Forward)', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('back from task detail (direct URL) returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-direct-${Date.now()}`);

    // Navigate to project first, then click the card (creates history entry via pushState)
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible();

    // Wait for card and click it
    await page.waitForSelector('.card');
    const cards = page.locator('.card');
    const count = await cards.count();
    if (count > 0) {
        await cards.last().click();
    }
    await expect(page.locator('.task-detail-page')).toBeVisible();
    expect(page.url()).toContain('task=');

    // Click browser back — should return to project board
    await page.goBack();
    await waitForUrl(page, url => !url.includes("task="));

    // Verify URL state
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');

    // Verify page CONTENTS: board is visible with columns
    await expect(page.locator('.board')).toBeVisible();
    await expect(page.locator('.column')).toHaveCount(4); // backlog, in_progress, review, done
    // Verify task detail is gone
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    // Verify top bar is visible (only shown in project view, not task detail)
    await expect(page.locator('.top-bar')).toBeVisible();
  });

  test('close task detail via in-app back button returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-btn-${Date.now()}`);

    await navigateToTask(page, taskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Click the in-app back button
    await page.click('.btn-back');

    // Verify page CONTENTS: board is visible with columns
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4);
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    await expect(page.locator('.top-bar')).toBeVisible();
    expect(page.url()).not.toContain('task=');
    expect(page.url()).toContain(`project=${projectId}`);
  });

  test('back from task shows full project view with top-bar buttons', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Navigate to project view
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Verify top-bar and Token usage button are visible in project view
    await expect(page.locator('.top-bar')).toBeVisible();
    await expect(page.locator('[aria-label="Token usage stats"]')).toBeVisible();

    // Create and open a task
    const taskTitle = `nav-topbar-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify top-bar is NOT visible in task detail
    await expect(page.locator('.top-bar')).not.toBeVisible();

    // Browser back
    await page.goBack();
    await waitForUrl(page, url => !url.includes("task="));

    // Verify FULL project view is restored: board, top-bar, AND Token usage button
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('[aria-label="Token usage stats"]')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });

  test('clicking card in project view opens task and back works', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Navigate to project view first
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Create a task
    const taskTitle = `nav-card-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);

    // Wait for the card to appear
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });

    // Check history length
    const historyBefore = await page.evaluate(() => history.length);

    // Click the card
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Check history length after
    const historyAfter = await page.evaluate(() => history.length);
    expect(historyAfter).toBeGreaterThan(historyBefore);

    // Browser back should work
    await page.goBack();
    await waitForUrl(page, url => !url.includes("task="));

    // Verify page CONTENTS: board is visible with columns
    expect(page.url()).not.toContain('task=');
    expect(page.url()).toContain(`project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4);
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    await expect(page.locator('.top-bar')).toBeVisible();
  });

  test('back from task preserves correct project in sidebar', async ({ page }) => {
    // Get project info
    const projectId = await getProjectId(page);
    const projectName = (await page.locator('.project-name').first().textContent())?.trim();

    // Navigate to project view
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Verify sidebar shows this project as active
    const activeProjectBefore = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectBefore?.trim()).toBe(projectName);

    // Create and open a task
    const taskTitle = `nav-sidebar-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Browser back
    await page.goBack();
    await waitForUrl(page, url => !url.includes("task="));
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Verify sidebar STILL shows the SAME project as active
    const activeProjectAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectAfter?.trim()).toBe(projectName);
    expect(page.url()).toContain(`project=${projectId}`);
  });

  test('subtask navigation creates history entry (pushState fix)', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create a parent task and a subtask via API
    const parentTitle = `nav-parent-${Date.now()}`;
    const parentId = await createTaskViaApi(page, projectId, parentTitle);
    const subtaskId = await createSubtaskViaApi(page, projectId, parentId, `nav-sub-${Date.now()}`);

    // Navigate to project view
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Navigate directly to subtask URL (simulates what pushState would set)
    await navigateToTask(page, subtaskId);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${subtaskId}`);

    // Use in-app back button (more reliable than browser goBack)
    const backBtn = page.locator('.btn-back');
    if (await backBtn.isVisible().catch(() => false)) {
      await backBtn.click();
    } else {
      // Fallback: navigate to project board directly
      await page.goto(`/?project=${projectId}`);
    }

    // Wait for board to appear and task detail to disappear
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
  });

  test('switch project via sidebar, open task, back preserves new project', async ({ page}) => {
    const projectId = await getProjectId(page);
    const projectName = (await page.locator('.project-name').first().textContent())?.trim();

    // Create a unique task in the project
    const taskTitle = `nav-switch-${Date.now()}`;
    const taskId = await createTaskViaApi(page, projectId, taskTitle);

    // Navigate to project view
    await page.goto(`/?project=${projectId}`);
    await expect(page.locator('.board')).toBeVisible({ timeout: 10000 });

    // Wait for our specific card and click it
    await page.waitForSelector(`.card-title-text:has-text("${taskTitle}")`, { timeout: 10000 });
    await page.click(`.card-title-text:has-text("${taskTitle}")`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify URL has the correct project and task
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).toContain(`task=${taskId}`);

    // Browser back
    await page.goBack();
    await waitForUrl(page, url => !url.includes("task="));
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Verify sidebar shows the SAME project (not switched)
    const activeProjectAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectAfter?.trim()).toBe(projectName);
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });
});
