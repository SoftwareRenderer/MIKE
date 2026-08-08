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
                },
                {
                    name: 'provider-b',
                    display_name: 'Provider B',
                    url: 'http://localhost:9998/v1',
                    models: [
                        { name: 'model-b1', reasoning_effort: 'low' },
                    ],
                    concurrency: 1,
                },
            ],
            worker_provider: 'provider-a',
            worker_model: 'model-a1',
            utility_provider: 'provider-a',
            utility_model: 'model-a1',
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
        // The model might be model-a1 or model-a depending on config state from parallel tests
        expect(taskData.model_name).toMatch(/^model-a/);

        // Wait for streaming to complete (send button visible means idle)
        // If the executor is running, wait for it to finish (or stop it if possible)
        // First ensure the chat form is loaded
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 15000 });
        await expect(async () => {
            const hasSend = await page.locator('.btn-send').isVisible();
            if (hasSend) return;
            // If stop button is visible, click it to cancel
            const hasStop = await page.locator('.btn-stop').isVisible();
            if (hasStop) {
                await page.locator('.btn-stop').click().catch(() => {});
            }
            // If neither, the form might still be loading
            throw new Error('Neither send nor stop button visible');
        }).toPass({ timeout: 15000 });
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

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

        // Wait for streaming to complete (send button visible means idle)
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 15000 });
        await expect(async () => {
            const hasSend = await page.locator('.btn-send').isVisible();
            if (hasSend) return;
            const hasStop = await page.locator('.btn-stop').isVisible();
            if (hasStop) await page.locator('.btn-stop').click().catch(() => {});
            throw new Error('Neither send nor stop button visible');
        }).toPass({ timeout: 15000 });
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

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

        // Wait for streaming to complete (send button visible means idle)
        await expect(page.locator('.chat-form')).toBeVisible({ timeout: 15000 });
        await expect(async () => {
            const hasSend = await page.locator('.btn-send').isVisible();
            if (hasSend) return;
            const hasStop = await page.locator('.btn-stop').isVisible();
            if (hasStop) await page.locator('.btn-stop').click().catch(() => {});
            throw new Error('Neither send nor stop button visible');
        }).toPass({ timeout: 15000 });
        await expect(page.locator('.btn-send')).toBeVisible({ timeout: 30000 });

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
        },
      ],
      worker_provider: 'delete-test',
      worker_model: 'alpha',
      utility_provider: 'delete-test',
      utility_model: 'alpha',
    });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'delete-test'
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should delete the correct model when models are displayed in sorted order', async ({ page }) => {
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal', { timeout: 5000 });

    const card = page.locator('.provider-card').first();
    // Expand card if collapsed
    if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
      await card.locator('.provider-card-header').click();
      await expect(card.locator('.provider-details')).toBeVisible();
    }

    // Verify models are displayed in alphabetical order
    const modelRows = card.locator('.model-row');
    const modelCount = await modelRows.count();
    expect(modelCount).toBeGreaterThanOrEqual(2);

    // Get the displayed model names (they should be sorted)
    const displayedNames: string[] = [];
    for (let i = 0; i < modelCount; i++) {
      const input = modelRows.nth(i).locator('.model-input');
      const label = modelRows.nth(i).locator('.model-name-label');
      if (await input.isVisible().catch(() => false)) {
        displayedNames.push(await input.inputValue());
      } else {
        displayedNames.push((await label.textContent())?.trim() || '');
      }
    }
    // Models should be in alphabetical order
    const sortedNames = [...displayedNames].sort();
    expect(displayedNames).toEqual(sortedNames);

    // Find and delete "mango" if present, otherwise delete the middle model
    let deletedIndex = -1;
    for (let i = 0; i < displayedNames.length; i++) {
      if (displayedNames[i] === 'mango') {
        deletedIndex = i;
        break;
      }
    }
    if (deletedIndex === -1 && displayedNames.length >= 2) {
      deletedIndex = Math.floor(displayedNames.length / 2);
    }

    if (deletedIndex >= 0 && deletedIndex < displayedNames.length) {
      const deletedName = displayedNames[deletedIndex];
      await modelRows.nth(deletedIndex).locator('.btn-remove-model').click();
      await expect(modelRows).toHaveCount(modelCount - 1, { timeout: 5000 });

      // Verify the remaining models don't include the deleted one
      const remainingNames: string[] = [];
      for (let i = 0; i < modelCount - 1; i++) {
        const input = modelRows.nth(i).locator('.model-input');
        const label = modelRows.nth(i).locator('.model-name-label');
        if (await input.isVisible().catch(() => false)) {
          remainingNames.push(await input.inputValue());
        } else {
          remainingNames.push((await label.textContent())?.trim() || '');
        }
      }
      expect(remainingNames).not.toContain(deletedName);
    }

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
            }],
            worker_provider: 'test-localhost-9999',
            worker_model: 'model-a',
            utility_provider: 'test-localhost-9999',
            utility_model: 'model-a',
        });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 1
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should show reasoning effort select for each model in settings', async ({ page }) => {
        // Open settings modal
        await page.click('.icon-btn[aria-label="Settings"]');
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
        await page.click('.icon-btn[aria-label="Settings"]');
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

        // Cancel — model edits mark the form dirty; dismiss the discard dialog.
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        // Wait for modal to close (replaces waitForTimeout(300))
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });

    test('should persist reasoning effort after save', async ({ page }) => {
        // Open settings
        await page.click('.icon-btn[aria-label="Settings"]');
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
            await page.click('.icon-btn[aria-label="Settings"]');
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

// ─── Primary Providers Tests ────────────────────────────────────────────────

test.describe.serial('Primary Providers', () => {
    test.beforeEach(async ({ page }) => {
        await page.keyboard.press('Escape').catch(() => {});
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

        // Set up two providers
        await apiPutConfig(page, {
            providers: [
                {
                    name: 'primary-a',
                    display_name: 'Primary A',
                    url: 'http://localhost:9999/v1',
                    models: [{ name: 'model-a1' }, { name: 'model-a2' }],
                    concurrency: 1,
                },
                {
                    name: 'primary-b',
                    display_name: 'Primary B',
                    url: 'http://localhost:9998/v1',
                    models: [{ name: 'model-b1' }],
                    concurrency: 1,
                },
            ],
            worker_provider: 'primary-a',
            worker_model: 'model-a1',
            utility_provider: 'primary-b',
            utility_model: 'model-b1',
        });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 2 && c.worker_provider === 'primary-a'
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should display Worker and Utility rows with provider/model dropdowns', async ({ page }) => {
        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Primary Providers section exists
        await expect(page.locator('.primary-provider-row')).toHaveCount(2);
        await expect(page.locator('.primary-provider-row').nth(0)).toContainText('Worker');
        await expect(page.locator('.primary-provider-row').nth(1)).toContainText('Utility');

        // Worker provider select has correct value
        const workerSelects = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select');
        await expect(workerSelects.nth(0)).toHaveValue('primary-a');
        await expect(workerSelects.nth(1)).toHaveValue('model-a1');

        // Utility provider select has correct value
        const utilitySelects = page.locator('.primary-provider-row').nth(1).locator('.primary-provider-select');
        await expect(utilitySelects.nth(0)).toHaveValue('primary-b');
        await expect(utilitySelects.nth(1)).toHaveValue('model-b1');
    });

    test('should block save when Worker pair is not selected', async ({ page }) => {
        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Clear the worker provider selection
        const workerProviderSelect = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select').nth(0);
        await workerProviderSelect.selectOption('');

        // Click Save — should be blocked with a validation error
        await page.click('button:has-text("Save")');
        await expect(page.locator('#config-error')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('#config-error')).toContainText('Worker');
        await expect(page.locator('.modal')).toBeVisible();
    });

    test('should persist worker/utility after save', async ({ page }) => {
        await page.click('.icon-btn[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Change worker model to model-a2
        const workerModelSelect = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select').nth(1);
        await workerModelSelect.selectOption('model-a2');

        await page.click('button:has-text("Save")');
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

        const config = await apiGetConfig(page);
        // Provider names are regenerated from URLs on save, so worker/utility
        // provider references must point at a provider matching the configured URL.
        expect(config.worker_model).toBe('model-a2');
        expect(config.utility_model).toBe('model-b1');
        const workerProvider = config.providers.find((p: any) => p.name === config.worker_provider);
        const utilityProvider = config.providers.find((p: any) => p.name === config.utility_provider);
        expect(workerProvider).toBeDefined();
        expect(workerProvider.url).toBe('http://localhost:9999/v1');
        expect(utilityProvider).toBeDefined();
        expect(utilityProvider.url).toBe('http://localhost:9998/v1');
    });
});
