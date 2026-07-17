import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, createTaskViaApi, navigateToTask, waitForChatForm, getTaskData, waitForSendButton } from './helpers';

// ─── Model Switcher Tests ───────────────────────────────────────────────────

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

// ─── Settings Model Delete Tests ────────────────────────────────────────────

test.describe.serial('Settings Model Delete', () => {
  test.beforeEach(async ({ page }) => {
    await page.keyboard.press('Escape').catch(() => {});
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

    // Set up a provider with models in non-alphabetical order
    // This exposes the bug where sorted display index != actual array index
    await apiPutConfig(page, {
      providers: [
        {
          name: 'delete-test',
          display_name: 'Delete Test',
          url: 'http://delete-test:9999/v1',
          models: ['zebra', 'alpha', 'mango'],
          concurrency: 1,
          default_model: '',
        },
      ],
      default_provider: '',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'delete-test'
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should delete the correct model when models are displayed in sorted order', async ({ page }) => {
    await page.locator('[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    // Expand card if collapsed
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Verify models are displayed in alphabetical order
    const modelRows = card.locator('.model-row');
    await expect(modelRows).toHaveCount(3);

    // Get the displayed model names (they should be sorted: alpha, mango, zebra)
    const displayedNames: string[] = [];
    for (let i = 0; i < 3; i++) {
      const input = modelRows.nth(i).locator('.model-input');
      const label = modelRows.nth(i).locator('.model-name-label');
      if (await input.isVisible().catch(() => false)) {
        displayedNames.push(await input.inputValue());
      } else {
        displayedNames.push((await label.textContent())?.trim() || '');
      }
    }
    expect(displayedNames).toEqual(['alpha', 'mango', 'zebra']);

    // Delete the middle model ("mango" at sorted index 1)
    await modelRows.nth(1).locator('.btn-remove-model').click();
    await expect(modelRows).toHaveCount(2, { timeout: 5000 });

    // Verify the remaining models are "alpha" and "zebra" (not "alpha" and "mango")
    const remainingNames: string[] = [];
    for (let i = 0; i < 2; i++) {
      const input = modelRows.nth(i).locator('.model-input');
      const label = modelRows.nth(i).locator('.model-name-label');
      if (await input.isVisible().catch(() => false)) {
        remainingNames.push(await input.inputValue());
      } else {
        remainingNames.push((await label.textContent())?.trim() || '');
      }
    }
    expect(remainingNames).toContain('alpha');
    expect(remainingNames).toContain('zebra');
    expect(remainingNames).not.toContain('mango');

    // Save and verify via API
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.name === 'delete-test-9999');
    expect(provider).toBeDefined();
    const savedModelNames = provider.models.map((m: any) => (typeof m === 'string' ? m : m.name));
    expect(savedModelNames).toContain('alpha');
    expect(savedModelNames).toContain('zebra');
    expect(savedModelNames).not.toContain('mango');
  });
});

// ─── Reasoning Effort Tests ─────────────────────────────────────────────────

test.describe.serial('Reasoning effort setting', () => {
    test.beforeEach(async ({ page }) => {
        // Ensure a provider exists (parallel tests may have cleared config)
        await apiPutConfig(page, {
            providers: [{
                name: 'test-localhost-9999',
                display_name: 'Test Server',
                url: 'http://localhost:9999/v1',
                models: ['model-a', 'model-b'],
                concurrency: 2,
                default_model: 'model-a',
            }],
            default_provider: 'test-localhost-9999',
        });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 1
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should show reasoning effort select for each model in settings', async ({ page }) => {
        // Open settings modal
        await page.click('[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Expand a provider card
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for reasoning selects to appear (replaces waitForTimeout(300))
        await expect(page.locator('.reasoning-effort-select').first()).toBeVisible({ timeout: 5000 });

        // Check that reasoning effort selects exist
        const reasoningSelects = page.locator('.reasoning-effort-select');
        const modelRows = page.locator('.model-row');
        const modelCount = await modelRows.count();

        if (modelCount > 0) {
            // Each model row should have a reasoning effort select
            expect(await reasoningSelects.count()).toBe(modelCount);

            // Verify the select has expected options using allOptions()
            const selectElement = reasoningSelects.first();
            const options = await selectElement.evaluateAll((selects: HTMLSelectElement[]) => {
                const select = selects[0];
                return Array.from(select.options).map(o => o.value);
            });
            expect(options).toContain('off');
            expect(options).toContain('low');
            expect(options).toContain('medium');
            expect(options).toContain('high');
            expect(options).toContain('max');
        }
    });

    test('should default to Medium for new models', async ({ page }) => {
        // Open settings
        await page.click('[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for details to appear (replaces waitForTimeout(200))
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

        // Add a new model
        await page.click('.btn-add-model');
        // Wait for new model input to appear (replaces waitForTimeout(200))
        const modelInputs = page.locator('.model-input');
        const inputCount = await modelInputs.count();

        // Fill in model name
        const modelInput = modelInputs.filter({ hasText: 'Model name' }).first();
        if (await modelInput.count() > 0) {
            await modelInput.fill(`test-model-${Date.now()}`);
        } else {
            await modelInputs.last().fill(`test-model-${Date.now()}`);
        }

        // Check that the reasoning effort select for the new row defaults to 'medium'
        const allSelects = page.locator('.reasoning-effort-select');
        const lastSelect = allSelects.last();
        const defaultValue = await lastSelect.inputValue();
        expect(defaultValue).toBe('medium');

        // Cancel without saving
        await page.click('button:has-text("Cancel")');
        // Wait for modal to close (replaces waitForTimeout(300))
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });

    test('should persist reasoning effort after save', async ({ page }) => {
        // Open settings
        await page.click('[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for details to appear (replaces waitForTimeout(200))
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

        const reasoningSelects = page.locator('.reasoning-effort-select');
        const count = await reasoningSelects.count();

        if (count > 0) {
            // Change first model's reasoning effort to 'high'
            await reasoningSelects.first().selectOption({ value: 'high' });

            // Save
            await page.click('button:has-text("Save")');
            // Wait for modal to close (replaces waitForTimeout(1000))
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

            // Reopen settings
            await page.click('[aria-label="Settings"]');
            await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

            // Expand first provider again
            await providerHeader.click();
            // Wait for details to appear (replaces waitForTimeout(300))
            await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

            // Check that the value persisted
            const savedSelect = page.locator('.reasoning-effort-select').first();
            const savedValue = await savedSelect.inputValue();
            expect(savedValue).toBe('high');

            // Change back to medium and save to restore
            await savedSelect.selectOption({ value: 'medium' });
            await page.click('button:has-text("Save")');
            // Wait for modal to close (replaces waitForTimeout(500))
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });
        }
    });
});
