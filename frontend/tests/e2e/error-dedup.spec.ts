import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch, navigateToTask } from './helpers';

// A provider that refuses connections. Port 9 (discard) has nothing listening.
const DEAD_PROVIDER_URL = 'http://127.0.0.1:9/v1';

// One unreachable-LLM failure used to grow a new card on every visit to the task:
// the failure frame stayed in the task's replay buffer, and each replay was drawn
// and stored again as if it were a fresh error.
test.describe.serial('repeated error messages', () => {
    test.beforeEach(async ({ page }) => {
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('one connection failure stays one card across revisits', async ({ page }) => {
        test.setTimeout(120_000);
        const projectId = await getProjectId(page);
        const prompt = `fail-connection-${Date.now()}`;

        const created = await csrfFetch(page, `/api/projects/${projectId}/tasks`, {
            method: 'POST',
            body: { prompt, llama_server_url: DEAD_PROVIDER_URL, model_name: 'no-such-model' },
        });
        expect(created.ok).toBe(true);
        const taskId = created.data?.id as string;
        expect(taskId).toBeTruthy();

        const started = await csrfFetch(page, `/api/projects/${projectId}/tasks/${taskId}/start`, { method: 'POST' });
        expect(started.ok).toBe(true);

        // Retries with backoff run first, so the failure takes a while to surface.
        await navigateToTask(page, taskId, projectId);
        const errorCard = page.locator('.error-card', { hasText: 'llm_connection_failed' });
        await expect(errorCard).toHaveCount(1, { timeout: 90_000 });

        for (let visit = 2; visit <= 3; visit++) {
            await page.evaluate((pid) => { window.location.href = `/?project=${pid}`; }, projectId);
            await expect(page.locator('.card').first()).toBeVisible();
            await navigateToTask(page, taskId, projectId);
            await expect(errorCard).toHaveCount(1, { timeout: 15_000 });
        }

        const storedErrors = await page.evaluate(async ({ pid, tid }) => {
            const res = await fetch(`/api/projects/${pid}/tasks/${tid}/messages`);
            const messages = await res.json();
            return messages.filter((m: any) => m.role === 'error').length;
        }, { pid: projectId, tid: taskId });
        expect(storedErrors).toBe(1);
    });
});
