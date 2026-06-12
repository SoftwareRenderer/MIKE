import { test, expect } from '@playwright/test';
import { apiPutConfig, waitForConfig } from './helpers';

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
