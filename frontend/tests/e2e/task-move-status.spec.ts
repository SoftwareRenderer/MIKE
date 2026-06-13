import { test, expect } from '@playwright/test';
import { getProjectId, createTaskViaApi } from './helpers';

test.describe.serial('Task move status buttons', () => {
    let projectId: string;
    let taskId: string;

    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.project-item', { timeout: 10000 });

        projectId = await getProjectId(page);
        taskId = await createTaskViaApi(page, projectId, `move-test-${Date.now()}`);

        // Open the task via URL
        await page.goto(`/?task=${taskId}`);
        await expect(page.locator('.task-detail-page')).toBeVisible({ timeout: 10000 });
    });

    test('shows move buttons when task is in Done', async ({ page }) => {
        // Move task to Done via API
        await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ status: 'done' }),
            });
        }, { pid: projectId, tid: taskId });

        // Wait for board to update via WebSocket
        await page.waitForTimeout(500);

        // The move bar should appear above the chat form
        await page.waitForSelector('.btn-move', { timeout: 10000 });

        const moveButtons = page.locator('.btn-move');
        await expect(moveButtons).toHaveCount(2);
        await expect(moveButtons.nth(0)).toContainText('→ Review');
        await expect(moveButtons.nth(1)).toContainText('→ Backlog');
    });

    test('shows move buttons when task is in Review', async ({ page }) => {
        // Move task to Review via API
        await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ status: 'review' }),
            });
        }, { pid: projectId, tid: taskId });

        // Wait for board to update
        await page.waitForTimeout(500);

        // The move bar should appear
        await page.waitForSelector('.btn-move', { timeout: 10000 });

        const moveButtons = page.locator('.btn-move');
        await expect(moveButtons).toHaveCount(2);
        await expect(moveButtons.nth(0)).toContainText('→ Done');
        await expect(moveButtons.nth(1)).toContainText('→ Backlog');

        // The commit button should still be present
        await expect(page.locator('.btn-commit-done')).toBeVisible();
    });

    test('moving from Done to Review via button works', async ({ page }) => {
        // Move task to Done via API
        await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            await fetch(`/api/projects/${pid}/tasks/${tid}/move`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ status: 'done' }),
            });
        }, { pid: projectId, tid: taskId });
        await page.waitForTimeout(500);

        // Click the "→ Review" button
        await page.waitForSelector('.btn-move:has-text("Review")', { timeout: 10000 });
        await page.locator('.btn-move:has-text("Review")').click();

        // Wait for the bar to update
        await page.waitForTimeout(1000);

        // Verify the task status via API
        const status = await page.evaluate(async ({ pid, tid }: { pid: string; tid: string }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}`);
            const task = await res.json();
            return task.status;
        }, { pid: projectId, tid: taskId });
        expect(status).toBe('review');
    });
});
