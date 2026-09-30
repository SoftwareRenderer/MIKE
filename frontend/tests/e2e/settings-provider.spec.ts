import { test, expect, type Page } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig, createTaskViaApi, navigateToTask, waitForChatForm, getTaskData, waitForSendButton } from './helpers';

/**
 * Seeds the given providers and then sets the worker/utility refs by the
 * server-assigned provider ID — the Settings modal resolves worker/utility
 * selections by provider ID, not by name. Returns a map of provider name → ID.
 */
async function seedProvidersByIds(
    page: Page,
    providers: any[],
    refs: { worker: string; workerModel: string; utility: string; utilityModel: string },
): Promise<Record<string, string>> {
    await apiPutConfig(page, { providers });
    const config = await apiGetConfig(page);
    const idByName: Record<string, string> = {};
    for (const p of config?.providers || []) {
        idByName[p.name] = String(p.id);
    }
    await apiPutConfig(page, {
        worker_provider: idByName[refs.worker],
        worker_model: refs.workerModel,
        utility_provider: idByName[refs.utility],
        utility_model: refs.utilityModel,
    });
    return idByName;
}

/**
 * Open Settings on the Providers tab. Which tab the control opens is the app's decision, not the
 * spec's: a Settings click lands on the tab the update dot points at (docs/auto-update.md), and on a
 * fresh test database the opt-in answer is owed, so the dot lights as soon as the status read lands
 * — possibly between two clicks of one test. A spec that works inside a provider card names the tab.
 */
async function openProviders(page: Page): Promise<void> {
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.locator('[role="dialog"]').first().waitFor({ state: 'visible', timeout: 5000 });
    await page.locator('button.tab-btn:has-text("Providers")').click();
}

// ─── Model Switcher Tests ───────────────────────────────────────────────────

test.describe.serial('Model switcher in task view', () => {
    test.beforeEach(async ({ page }) => {
        // Set up config with two providers; worker/utility refs are the
        // server-assigned provider IDs (the Settings modal resolves by ID).
        const idByName = await seedProvidersByIds(page, [
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
        ], { worker: 'provider-a', workerModel: 'model-a1', utility: 'provider-a', utilityModel: 'model-a1' });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 2 && c.worker_provider === idByName['provider-a']
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

        // Select provider-b (LLM display names are derived from the URL: host:port)
        await page.click('.provider-option:has-text("localhost:9998")');

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
    // This exposes the bug where sorted display index != actual array index.
    // Worker/utility refs are the server-assigned provider IDs (the Settings
    // modal resolves by ID, not by name).
    const idByName = await seedProvidersByIds(page, [
      {
        name: 'delete-test',
        display_name: 'Delete Test',
        url: 'http://delete-test:9999/v1',
        models: ['zebra', 'alpha', 'mango'],
        concurrency: 1,
      },
    ], { worker: 'delete-test', workerModel: 'alpha', utility: 'delete-test', utilityModel: 'alpha' });
    await waitForConfig(page, c =>
      c.providers && c.providers.length === 1 && c.providers[0].name === 'delete-test' && c.worker_provider === idByName['delete-test']
    );
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should delete the correct model when models are displayed in sorted order', async ({ page }) => {
    await openProviders(page);

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

    // The UI save clears LLM provider names (display names are derived from the
    // URL), so look the provider up by its URL.
    const config = await apiGetConfig(page);
    const provider = config.providers.find((p: any) => p.url === 'http://delete-test:9999/v1');
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
        // Ensure a provider exists (parallel tests may have cleared config).
        // Worker/utility refs are the server-assigned provider IDs (the
        // Settings modal resolves by ID, not by name).
        const idByName = await seedProvidersByIds(page, [{
            name: 'test-localhost-9999',
            display_name: 'Test Server',
            url: 'http://localhost:9999/v1',
            models: ['model-a', 'model-b'],
            concurrency: 2,
        }], { worker: 'test-localhost-9999', workerModel: 'model-a', utility: 'test-localhost-9999', utilityModel: 'model-a' });
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 1 && c.worker_provider === idByName['test-localhost-9999']
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should show reasoning effort suggestions when the input has focus', async ({ page }) => {
        // Open settings modal
        await openProviders(page);

        // Expand a provider card
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for reasoning effort inputs to appear (replaces waitForTimeout(300))
        await expect(page.locator('.reasoning-effort-input').first()).toBeVisible({ timeout: 5000 });

        // Check that reasoning effort inputs exist
        const reasoningInputs = page.locator('.reasoning-effort-input');
        const modelRows = page.locator('.model-row');
        const modelCount = await modelRows.count();

        if (modelCount > 0) {
            // Each model row should have a reasoning effort input
            expect(await reasoningInputs.count()).toBe(modelCount);

            // Focusing the input opens the suggestion list with the known levels
            await reasoningInputs.first().focus();
            const optionList = page.locator('.reasoning-effort-list');
            await expect(optionList).toBeVisible({ timeout: 5000 });
            const options = page.locator('.reasoning-effort-list .reasoning-effort-option');
            const labels = await options.evaluateAll(opts => opts.map(o => o.textContent || ''));
            expect(labels).toEqual(['Default', 'None', 'Low', 'Medium', 'High']);

            // Typing filters the suggestions
            await reasoningInputs.first().fill('med');
            const filtered = await options.evaluateAll(opts => opts.map(o => o.textContent || ''));
            expect(filtered).toEqual(['Medium']);

            // Restore the value and close the list
            await reasoningInputs.first().fill('default');
            await page.keyboard.press('Escape');
            await expect(optionList).toBeHidden({ timeout: 5000 });
        }
    });

    test('should default to Default for new models', async ({ page }) => {
        // Open settings
        await openProviders(page);

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for details to appear (replaces waitForTimeout(200))
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

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

        // Check that the reasoning effort input for the new row defaults to 'default'
        const allInputs = page.locator('.reasoning-effort-input');
        const lastInput = allInputs.last();
        const defaultValue = await lastInput.inputValue();
        expect(defaultValue).toBe('default');

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
        await openProviders(page);

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        // Wait for details to appear (replaces waitForTimeout(200))
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

        const reasoningInputs = page.locator('.reasoning-effort-input');
        const count = await reasoningInputs.count();

        if (count > 0) {
            // Change first model's reasoning effort to 'high' via the suggestion list
            const effortInput = reasoningInputs.first();
            await effortInput.focus();
            await page.locator('.reasoning-effort-list .reasoning-effort-option', { hasText: 'High' }).click();
            await expect(effortInput).toHaveValue('high');

            await page.click('button:has-text("Save")');
            // Wait for modal to close (replaces waitForTimeout(1000))
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

            // Reopen settings
            await openProviders(page);

            // Expand first provider again
            await providerHeader.click();
            // Wait for details to appear (replaces waitForTimeout(300))
            await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });

            // Check that the value persisted
            const savedInput = page.locator('.reasoning-effort-input').first();
            const savedValue = await savedInput.inputValue();
            expect(savedValue).toBe('high');

            // Change to 'default' and save to verify it also round-trips
            await savedInput.fill('default');
            await page.click('button:has-text("Save")');
            // Wait for modal to close (replaces waitForTimeout(500))
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

            // Reopen settings and verify 'default' persisted
            await openProviders(page);
            await providerHeader.click();
            await expect(page.locator('.provider-details')).toBeVisible({ timeout: 3000 });
            const finalValue = await page.locator('.reasoning-effort-input').first().inputValue();
            expect(finalValue).toBe('default');

            // Close settings
            await page.click('button:has-text("Cancel")');
            const discardBtn = page.getByRole('button', { name: 'Discard' });
            if (await discardBtn.isVisible().catch(() => false)) {
                await discardBtn.click();
            }
            await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
        }
    });

    test('should persist a custom reasoning effort after save', async ({ page }) => {
        // Open settings
        await openProviders(page);

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        const reasoningInputs = page.locator('.reasoning-effort-input');
        if (await reasoningInputs.count() === 0) return;

        // Type a value that is not one of the known levels (no suggestion matches)
        await reasoningInputs.first().fill('xhigh');
        await expect(reasoningInputs.first()).toHaveValue('xhigh');
        await expect(page.locator('.reasoning-effort-list')).toBeHidden();

        await page.click('button:has-text("Save")');
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

        // The custom value must round-trip through the config API (model-a is the
        // first model). The UI save clears LLM provider names, so look up by URL.
        const config = await apiGetConfig(page);
        const provider = config.providers.find((p: any) => p.url === 'http://localhost:9999/v1');
        expect(provider).toBeDefined();
        const modelA = provider.models.find((m: any) => (typeof m === 'string' ? m : m.name) === 'model-a');
        expect(modelA).toBeDefined();
        expect(modelA.reasoning_effort).toBe('xhigh');

        // Reopen settings and verify the input shows the custom value
        await openProviders(page);
        await providerHeader.click();
        await expect(page.locator('.provider-details')).toBeVisible({ timeout: 5000 });
        await expect(page.locator('.reasoning-effort-input').first()).toHaveValue('xhigh');

        // Close settings (beforeEach resets the config for the next test)
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});

// ─── Primary Providers Tests ────────────────────────────────────────────────

test.describe.serial('Primary Providers', () => {
    // Server-assigned provider IDs (the modal resolves worker/utility by ID).
    let workerProviderId = '';
    let utilityProviderId = '';

    test.beforeEach(async ({ page }) => {
        await page.keyboard.press('Escape').catch(() => {});
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

        // Set up two providers; worker/utility refs are the assigned IDs.
        const idByName = await seedProvidersByIds(page, [
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
        ], { worker: 'primary-a', workerModel: 'model-a1', utility: 'primary-b', utilityModel: 'model-b1' });
        workerProviderId = idByName['primary-a'];
        utilityProviderId = idByName['primary-b'];
        await waitForConfig(page, c =>
            c.providers && c.providers.length >= 2 && c.worker_provider === workerProviderId
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should display Worker and Utility rows with provider/model dropdowns', async ({ page }) => {
        await openProviders(page);

        // Primary Providers section exists
        await expect(page.locator('.primary-provider-row')).toHaveCount(2);
        await expect(page.locator('.primary-provider-row').nth(0)).toContainText('Worker');
        await expect(page.locator('.primary-provider-row').nth(1)).toContainText('Utility');

        // Worker provider select has the assigned ID as its value
        const workerSelects = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select');
        await expect(workerSelects.nth(0)).toHaveValue(workerProviderId);
        await expect(workerSelects.nth(1)).toHaveValue('model-a1');

        // Utility provider select has correct value
        const utilitySelects = page.locator('.primary-provider-row').nth(1).locator('.primary-provider-select');
        await expect(utilitySelects.nth(0)).toHaveValue(utilityProviderId);
        await expect(utilitySelects.nth(1)).toHaveValue('model-b1');
    });

    test('should block save when Worker pair is not selected', async ({ page }) => {
        await openProviders(page);

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
        await openProviders(page);

        // Change worker model to model-a2
        const workerModelSelect = page.locator('.primary-provider-row').nth(0).locator('.primary-provider-select').nth(1);
        await workerModelSelect.selectOption('model-a2');

        await page.click('button:has-text("Save")');
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

        const config = await apiGetConfig(page);
        // Provider names are cleared for LLM providers on UI save, so worker/
        // utility provider references (provider IDs) must resolve to a provider
        // matching the configured URL.
        expect(config.worker_model).toBe('model-a2');
        expect(config.utility_model).toBe('model-b1');
        const workerProvider = config.providers.find((p: any) => String(p.id) === config.worker_provider);
        const utilityProvider = config.providers.find((p: any) => String(p.id) === config.utility_provider);
        expect(workerProvider).toBeDefined();
        expect(workerProvider.url).toBe('http://localhost:9999/v1');
        expect(utilityProvider).toBeDefined();
        expect(utilityProvider.url).toBe('http://localhost:9998/v1');
    });
});

// ─── Provider max requests per second Tests ──────────────────────────────────

test.describe.serial('Provider max requests per second', () => {
    // Unique per-run identifiers to avoid interference between runs
    const ts = Date.now();
    const seedUrl = `http://rps-seed-${ts}/v1`;
    const seedModel = 'rps-model-a';

    /**
     * Seed the isolated server with a single LLM provider carrying the given
     * max_requests_per_second. The server assigns a stable integer ID on the
     * first PUT, so references are set in a second PUT keyed by that ID (the
     * Settings modal resolves worker/utility selections by provider ID).
     */
    async function seedProviderConfig(page, maxRps: number) {
        await apiPutConfig(page, {
            providers: [
                {
                    url: seedUrl,
                    models: [{ name: seedModel }],
                    concurrency: 2,
                    max_requests_per_second: maxRps,
                },
            ],
        });
        const config = await apiGetConfig(page);
        const id = String(config?.providers?.find((p: any) => p.url === seedUrl)?.id ?? '');
        expect(id).not.toBe('');
        await apiPutConfig(page, {
            worker_provider: id,
            worker_model: seedModel,
            utility_provider: id,
            utility_model: seedModel,
        });
        await waitForConfig(page, c =>
            c.providers && c.worker_provider === id &&
            c.providers.find((p: any) => p.url === seedUrl)?.max_requests_per_second === maxRps
        );
    }

    test.beforeEach(async ({ page }) => {
        await page.keyboard.press('Escape').catch(() => {});
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

        await seedProviderConfig(page, 5);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should round-trip max_requests_per_second through config API', async ({ page }) => {
        // beforeEach seeded max_requests_per_second: 5; the server must persist and
        // serialize it verbatim (plain int, no omitempty).
        const config = await apiGetConfig(page);
        const provider = config.providers.find((p: any) => p.url === seedUrl);
        expect(provider).toBeDefined();
        expect(provider.max_requests_per_second).toBe(5);
    });

    test('should show and persist Max requests/sec in the provider card UI', async ({ page }) => {
        // Open Settings (seeded provider shows max_requests_per_second: 5)
        await openProviders(page);

        // Expand the (single) provider card
        const card = page.locator('.provider-card').first();
        if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
            await card.locator('.provider-card-header').click();
        }
        await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        // The LLM branch shows a Max requests/sec input seeded with 5
        const maxRpsInput = page.getByLabel('Max requests/sec');
        await expect(maxRpsInput).toBeVisible({ timeout: 5000 });
        await expect(maxRpsInput).toHaveValue('5');

        // Change to 3 and save
        await maxRpsInput.fill('3');
        await page.getByRole('button', { name: 'Save' }).click();
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 10000 });

        // Persisted value verified via API
        const config = await apiGetConfig(page);
        const provider = config.providers.find((p: any) => p.url === seedUrl);
        expect(provider).toBeDefined();
        expect(provider.max_requests_per_second).toBe(3);

        // Re-open Settings — the card shows the saved value
        await openProviders(page);
        if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
            await card.locator('.provider-card-header').click();
        }
        await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });
        await expect(page.getByLabel('Max requests/sec')).toHaveValue('3');

        // Close without further changes
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });

    test('should default to 0 for a newly added provider', async ({ page }) => {
        // Open Settings
        await openProviders(page);

        // Add a fresh LLM provider — the new card is expanded by default
        await page.getByRole('button', { name: '+ Add Provider' }).click();
        const newCard = page.locator('.provider-card').last();
        await expect(newCard.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        // The new card's Max requests/sec input defaults to 0
        const inputs = newCard.getByLabel('Max requests/sec');
        await expect(inputs).toHaveCount(1, { timeout: 5000 });
        await expect(inputs).toHaveValue('0');

        // Discard the unsaved provider
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});

// ─── Test Connection Auto-Fetch Tests ────────────────────────────────────

test.describe.serial('Test connection with empty model list', () => {
    test.beforeEach(async ({ page }) => {
        await page.keyboard.press('Escape').catch(() => {});
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});

        // Single provider pointing at the mock LLM (serves /v1/models and
        // /v1/chat/completions) with an EMPTY model list — the "fetch models
        // never ran" state.
        const idByName = await seedProvidersByIds(page, [
            { name: 'auto-fetch', url: 'http://localhost:9999/v1', models: [], concurrency: 1 },
        ], { worker: 'auto-fetch', workerModel: 'mock-model', utility: 'auto-fetch', utilityModel: 'mock-model' });
        await waitForConfig(page, c =>
            c.providers && c.providers.length === 1 && c.worker_provider === idByName['auto-fetch']
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });
    });

    test('should auto-fetch models before testing the connection', async ({ page }) => {
        // Record the browser-level request order to prove fetch-models runs
        // before test-connection when the model list is empty.
        const requestOrder: string[] = [];
        page.on('request', req => {
            if (req.url().includes('/api/config/fetch-models')) requestOrder.push('fetch-models');
            if (req.url().includes('/api/config/test-connection')) requestOrder.push('test-connection');
        });

        await openProviders(page);

        const card = page.locator('.provider-card').first();
        if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
            await card.locator('.provider-card-header').click();
        }
        await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        // The model list starts empty (no rows).
        await expect(card.locator('.model-row')).toHaveCount(0);

        // Press Test Connection without ever clicking Fetch Models.
        await card.locator('.btn-test').click();

        // The mock LLM serves both endpoints, so the test must succeed.
        await expect(card.locator('.test-result.test-success')).toBeVisible({ timeout: 15000 });
        await expect(card.locator('.test-result.test-success')).toContainText('Connection successful!');

        // The model list was populated by the automatic fetch (mock LLM
        // returns three models) — no separate Fetch Models click happened.
        await expect(card.locator('.model-row')).toHaveCount(3, { timeout: 5000 });

        // Fetch models must have run before the connection test.
        expect(requestOrder.indexOf('fetch-models')).toBeGreaterThanOrEqual(0);
        expect(requestOrder.indexOf('fetch-models')).toBeLessThan(requestOrder.indexOf('test-connection'));

        // Close the modal (discard any unsaved state just in case)
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });

    test('should not re-fetch when the model list is already populated', async ({ page }) => {
        // Re-seed with a populated model list (the "fetch already ran" state).
        await apiPutConfig(page, {
            providers: [{ name: 'pre-populated', url: 'http://localhost:9999/v1', models: [{ name: 'model-x' }], concurrency: 1 }],
        });
        const config = await apiGetConfig(page);
        const id = String(config?.providers?.find((p: any) => p.name === 'pre-populated')?.id ?? '');
        await apiPutConfig(page, {
            worker_provider: id, worker_model: 'model-x',
            utility_provider: id, utility_model: 'model-x',
        });
        await waitForConfig(page, c => c.providers && c.worker_provider === id);
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        const requestOrder: string[] = [];
        page.on('request', req => {
            if (req.url().includes('/api/config/fetch-models')) requestOrder.push('fetch-models');
            if (req.url().includes('/api/config/test-connection')) requestOrder.push('test-connection');
        });

        await openProviders(page);

        const card = page.locator('.provider-card').first();
        if (!await card.locator('.provider-details').isVisible().catch(() => false)) {
            await card.locator('.provider-card-header').click();
        }
        await expect(card.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        // One model row exists from the seed.
        await expect(card.locator('.model-row')).toHaveCount(1);

        await card.locator('.btn-test').click();

        // The test succeeds (mock LLM serves /v1/chat/completions) and uses the
        // existing model — the list is untouched (still exactly one row).
        await expect(card.locator('.test-result.test-success')).toBeVisible({ timeout: 15000 });
        await expect(card.locator('.model-row')).toHaveCount(1);

        // No fetch-models request at all.
        expect(requestOrder).toEqual(['test-connection']);

        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});

// ─── Add Provider Scroll Tests ─────────────────────────────────────────────
// Only the positive half runs here: the negative half is a call to `scrollIntoView`, recorded in
// `src/lib/components/__tests__/config-modal-provider-scroll.unit.svelte.ts`. The browser version measured
// layout rounding, where a fit 4 px from the edge failed the spec rather than the rule.

test.describe.serial('Add provider scroll', () => {
    test.beforeEach(async ({ page }) => {
        await page.keyboard.press('Escape').catch(() => {});
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 2000 }).catch(() => {});
    });

    test('should scroll the new provider input area into view and focus the URL input', async ({ page }) => {
        // Two providers with many models each — expanded, they make the
        // providers list far taller than the modal's visible area.
        const manyModels = (prefix: string) =>
            Array.from({ length: 12 }, (_, i) => ({ name: `${prefix}-model-${i}` }));
        const idByName = await seedProvidersByIds(page, [
            { name: 'scroll-a', url: 'http://scroll-a:9999/v1', models: manyModels('a'), concurrency: 1 },
            { name: 'scroll-b', url: 'http://scroll-b:9999/v1', models: manyModels('b'), concurrency: 1 },
        ], { worker: 'scroll-a', workerModel: 'a-model-0', utility: 'scroll-a', utilityModel: 'a-model-0' });
        await waitForConfig(page, c =>
            c.providers && c.providers.length === 2 && c.worker_provider === idByName['scroll-a']
        );
        await page.goto('/');
        await page.waitForSelector('.card', { timeout: 10000 });

        await openProviders(page);

        // Expand both provider cards so the list extends well past the fold.
        const cards = page.locator('.provider-card');
        for (let i = 0; i < (await cards.count()); i++) {
            const details = cards.nth(i).locator('.provider-details');
            if (!await details.isVisible().catch(() => false)) {
                await cards.nth(i).locator('.provider-card-header').click();
            }
            await expect(details).toBeVisible({ timeout: 5000 });
        }

        // Scroll the modal back to the top — the bottom of the list (where a
        // new card would land) is now out of view. (Index-based locator: .last()
        // would re-resolve to the new card after it is added.)
        await page.$eval('.modal-body', el => (el.scrollTop = 0));
        await expect(cards.nth(1).locator('.provider-details')).not.toBeInViewport();

        // Add a fresh LLM provider — its input area must be scrolled into view.
        await page.getByRole('button', { name: '+ Add Provider' }).click();
        const newCard = page.locator('.provider-card').last();
        await expect(newCard.locator('.provider-details')).toBeVisible({ timeout: 5000 });

        const urlInput = newCard.getByLabel('Provider URL');
        await expect(urlInput).toBeInViewport({ timeout: 5000 });
        await expect(urlInput).toBeFocused();

        // Discard the unsaved provider
        await page.click('button:has-text("Cancel")');
        const discardBtn = page.getByRole('button', { name: 'Discard' });
        if (await discardBtn.isVisible().catch(() => false)) {
            await discardBtn.click();
        }
        await expect(page.locator('.modal')).not.toBeVisible({ timeout: 5000 });
    });
});
