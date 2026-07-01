import { test, expect } from '@playwright/test';
import { apiPutConfig, waitForConfig, createTaskViaApi, navigateToTask, waitForChatForm, getTaskData, csrfFetch, waitForSendButton } from './helpers';

test.describe.serial('Model switcher in task view', () => {
    test.beforeEach(async ({ page }) => {
        // Set up config with two providers
        await apiPutConfig(page, {
            providers: [
                {
                    name: 'provider-a',
                    display_name: 'Provider A',
                    url: 'http://localhost:9999/v1',
                    models: [
                        { name: 'model-a1', reasoning_effort: 'medium' },
                        { name: 'model-a2', reasoning_effort: 'high' },
                    ],
                    concurrency: 2,
                    default_model: 'model-a1',
                },
                {
                    name: 'provider-b',
                    display_name: 'Provider B',
                    url: 'http://localhost:9998/v1',
                    models: [
                        { name: 'model-b1', reasoning_effort: 'low' },
                    ],
                    concurrency: 1,
                    default_model: 'model-b1',
                },
            ],
            default_provider: 'provider-a',
        });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 2
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should update task model when switching provider in chat form', async ({ page }) => {
        // Create and open a task
        const taskId = await createTaskViaApi(page, await page.evaluate(async () => {
            const res = await fetch('/api/projects');
            return (await res.json())[0]?.id;
        }), `model-switch-test-${Date.now()}`);
        await navigateToTask(page, taskId);
        await waitForChatForm(page);

        const projectId = await page.evaluate(async () => {
            const params = new URLSearchParams(window.location.search);
            return params.get('project') || '';
        });

        // Verify initial model is from provider-a (default)
        let taskData = await getTaskData(page, projectId, taskId);
        expect(taskData.model_name).toBe('model-a1');

        // Stop any running LLM response, then wait for send button
        if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
            await page.click('.btn-stop');
        }
        await waitForSendButton(page);

        // Open the provider dropdown
        await page.click('.provider-trigger');
        await page.waitForSelector('.portal-provider-dropdown', { timeout: 5000 });

        // Select provider-b
        await page.click('.provider-option:has-text("Provider B")');

        // Wait for the provider dropdown to close and model to update
        await expect(page.locator('.portal-provider-dropdown')).toBeHidden({ timeout: 5000 });

        // Wait a moment for updateTask to complete
        await page.waitForTimeout(500);

        // Verify the task model was updated via API
        taskData = await getTaskData(page, projectId, taskId);
        expect(taskData.model_name).toBe('model-b1');
        expect(taskData.llama_server_url).toBe('http://localhost:9998/v1');
    });

    test('should update task model when switching model in chat form', async ({ page }) => {
        // Create and open a task
        const taskId = await createTaskViaApi(page, await page.evaluate(async () => {
            const res = await fetch('/api/projects');
            return (await res.json())[0]?.id;
        }), `model-switch-test-${Date.now()}`);
        await navigateToTask(page, taskId);
        await waitForChatForm(page);

        const projectId = await page.evaluate(async () => {
            const params = new URLSearchParams(window.location.search);
            return params.get('project') || '';
        });

        // Verify initial model is model-a1
        let taskData = await getTaskData(page, projectId, taskId);
        expect(taskData.model_name).toBe('model-a1');

        // Stop any running LLM response
        if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
            await page.click('.btn-stop');
        }
        await waitForSendButton(page);

        // Open the model dropdown
        await page.click('.model-trigger');
        await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });

        // Select model-a2 (from same provider, different model)
        await page.click('.model-option:has-text("model-a2")');

        // Wait for the model dropdown to close
        await expect(page.locator('.portal-model-dropdown')).toBeHidden({ timeout: 5000 });

        // Wait for updateTask to complete
        await page.waitForTimeout(500);

        // Verify the task model was updated
        taskData = await getTaskData(page, projectId, taskId);
        expect(taskData.model_name).toBe('model-a2');
    });

    test('should include reasoning_effort in send request', async ({ page }) => {
        // Create and open a task
        const taskId = await createTaskViaApi(page, await page.evaluate(async () => {
            const res = await fetch('/api/projects');
            return (await res.json())[0]?.id;
        }), `model-switch-test-${Date.now()}`);
        await navigateToTask(page, taskId);
        await waitForChatForm(page);

        const projectId = await page.evaluate(async () => {
            const params = new URLSearchParams(window.location.search);
            return params.get('project') || '';
        });

        // Stop any running LLM response
        if (await page.locator('.btn-stop').isVisible().catch(() => false)) {
            await page.click('.btn-stop');
        }
        await waitForSendButton(page);

        // Switch to model-a2 which has reasoning_effort: high
        await page.click('.model-trigger');
        await page.waitForSelector('.portal-model-dropdown', { timeout: 5000 });
        await page.click('.model-option:has-text("model-a2")');
        await expect(page.locator('.portal-model-dropdown')).toBeHidden({ timeout: 5000 });
        await page.waitForTimeout(500);

        // Intercept the /send request to verify it includes reasoning_effort
        const sendPromise = page.waitForResponse(resp =>
            resp.url().includes('/send') && resp.status() === 201
        );

        // Send a message
        const chatTextarea = page.locator('textarea[placeholder*="message"]');
        await chatTextarea.fill(`test-message-${Date.now()}`);
        await chatTextarea.press('Enter');

        const response = await sendPromise;
        const request = response.request();
        const postData = request.postData();

        // Verify the request body includes reasoning_effort
        expect(postData).toContain('"reasoning_effort"');
        expect(postData).toContain('"high"');
        expect(postData).toContain('"model_name"');
        expect(postData).toContain('"model-a2"');
    });
});
