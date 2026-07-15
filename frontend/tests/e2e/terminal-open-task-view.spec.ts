import { test, expect } from '@playwright/test';
import { getProjectId, navigateToTask, createTaskViaApi, ensureTaskStarted } from './helpers';

test.describe('Terminal open from task view', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('terminal panel opens when clicking terminal button in task header', async ({ page }) => {
        // 1. Create a task and start it (needs a branch for terminal button to be enabled)
        const projectId = await getProjectId(page);
        const taskId = await createTaskViaApi(page, projectId, `terminal-test-${Date.now()}`);
        const started = await ensureTaskStarted(page, projectId, taskId);
        test.skip(!started, 'Could not start task');

        // 2. Navigate to the task detail page
        await navigateToTask(page, taskId, projectId);

        // 3. Wait for the task detail page to load
        await expect(page.locator('.task-detail-page')).toBeVisible();

        // 4. Find the terminal button in the task header (title="Open terminal")
        const terminalButton = page.locator('button[title="Open terminal"]');
        await expect(terminalButton).toBeVisible();

        // 5. Verify terminal is not visible before clicking
        await expect(page.locator('.terminal-panel')).not.toBeVisible();

        // 6. Click the terminal button
        await terminalButton.click();

        // 7. Verify the terminal panel appears (lazy-loaded chunk + showTerminal flag)
        await expect(page.locator('.terminal-panel')).toBeVisible({ timeout: 10000 });
    });
});
