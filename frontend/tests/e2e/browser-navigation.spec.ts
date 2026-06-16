import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

// Helper: create a subtask via API
async function createSubtaskViaApi(page, projectId: string, parentTaskId: string, prompt: string): Promise<string> {
    return await page.evaluate(async ({ pid, parentId, prompt }) => {
        const [configRes] = await Promise.all([
            fetch('/api/config'),
        ]);
        const config = await configRes.json();
        const defaultProvider = config.providers?.find((p: any) => p.name === config.default_provider);

        const taskRes = await fetch(`/api/projects/${pid}/tasks`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                prompt,
                parent_task_id: parentId,
                llama_server_url: defaultProvider?.url || '',
                model_name: defaultProvider?.default_model || '',
            }),
        });
        const task = await taskRes.json();
        return task.id;
    }, { pid: projectId, parentId: parentTaskId, prompt });
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

    // Navigate directly to task URL (creates initial history entry)
    await page.goto(`/?project=${projectId}&task=${taskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${taskId}`);

    // Click browser back — should return to project board
    await page.goBack();
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });

    // Verify URL state
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');

    // Verify page CONTENTS: board is visible with columns
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.column')).toHaveCount(4); // backlog, in_progress, review, done
    // Verify task detail is gone
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    // Verify top bar is visible (only shown in project view, not task detail)
    await expect(page.locator('.top-bar')).toBeVisible();
  });

  test('close task detail via in-app back button returns to project board', async ({ page }) => {
    const projectId = await getProjectId(page);
    const taskId = await createTaskViaApi(page, projectId, `nav-btn-${Date.now()}`);

    await page.goto(`/?project=${projectId}&task=${taskId}`);
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
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });

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
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });

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
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });
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
    await page.goto(`/?project=${projectId}&task=${subtaskId}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${subtaskId}`);

    // Browser back — should return to project board
    await page.goBack();
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });

    // Verify FULL project view restored
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('[aria-label="Token usage stats"]')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });

  test('switching tasks from CardDetail sidebar works with back button', async ({ page }) => {
    const projectId = await getProjectId(page);

    // Create two tasks
    const task1Id = await createTaskViaApi(page, projectId, `nav-switch1-${Date.now()}`);
    await createTaskViaApi(page, projectId, `nav-switch2-${Date.now() + 1}`);

    // Navigate to first task
    await page.goto(`/?project=${projectId}&task=${task1Id}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    expect(page.url()).toContain(`task=${task1Id}`);

    // Wait for task list cards in CardDetail sidebar (.column-context has the board overview)
    await page.waitForSelector('.column-context .card', { timeout: 10000 });

    // Find a card that's NOT the current task (click any other card in the sidebar)
    const sidebarCards = await page.locator('.column-context .card').all();
    let targetCard = null;
    for (const card of sidebarCards) {
        const titleEl = card.locator('.card-title-text');
        const title = await titleEl.textContent();
        if (title && !title.includes(task1Id)) {
            targetCard = card;
            break;
        }
    }
    // If no different card found (consolidation), just click any other card
    if (!targetCard && sidebarCards.length > 1) {
        targetCard = sidebarCards[1];
    }
    expect(targetCard).not.toBeNull();

    // Click to switch tasks
    await targetCard.click();
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });

    // Verify URL changed to a different task
    const urlAfterSwitch = page.url();
    expect(urlAfterSwitch).not.toContain(`task=${task1Id}`);
    expect(urlAfterSwitch).toContain('task=');

    // Browser back — should return to first task
    await page.goBack();
    await page.waitForURL(url => url.href.includes(`task=${task1Id}`), { timeout: 5000 });
    expect(page.url()).toContain(`task=${task1Id}`);
    await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 5000 });

    // Browser back again — should return to project board
    await page.goBack();
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.top-bar')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('[aria-label="Token usage stats"]')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.task-detail-page')).not.toBeVisible();
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });

  test('switch project via sidebar, open task, back preserves new project', async ({ page }) => {
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
    await page.waitForURL(url => !url.href.includes('task='), { timeout: 5000 });
    await expect(page.locator('.board')).toBeVisible({ timeout: 5000 });

    // Verify sidebar shows the SAME project (not switched)
    const activeProjectAfter = await page.locator('.project-item-wrapper.active .project-name').textContent();
    expect(activeProjectAfter?.trim()).toBe(projectName);
    expect(page.url()).toContain(`project=${projectId}`);
    expect(page.url()).not.toContain('task=');
  });
});
