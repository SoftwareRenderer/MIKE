import { test, expect } from '@playwright/test';

test.describe.serial('Reasoning effort setting', () => {
    test.beforeEach(async ({ page }) => {
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
        await page.waitForTimeout(300);

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
        await page.waitForTimeout(200);

        // Add a new model
        await page.click('.btn-add-model');
        await page.waitForTimeout(200);

        // Fill in model name
        const modelInput = page.locator('.model-input').filter({ hasText: 'Model name' }).first();
        if (await modelInput.count() > 0) {
            await modelInput.fill(`test-model-${Date.now()}`);
        } else {
            await page.locator('.model-input').last().fill(`test-model-${Date.now()}`);
        }

        // Check that the reasoning effort select for the new row defaults to 'medium'
        const allSelects = page.locator('.reasoning-effort-select');
        const lastSelect = allSelects.last();
        const defaultValue = await lastSelect.inputValue();
        expect(defaultValue).toBe('medium');

        // Cancel without saving
        await page.click('button:has-text("Cancel")');
        await page.waitForTimeout(300);
    });

    test('should persist reasoning effort after save', async ({ page }) => {
        // Open settings
        await page.click('[aria-label="Settings"]');
        await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

        // Expand first provider
        const providerHeader = page.locator('.provider-card-header').first();
        await providerHeader.click();
        await page.waitForTimeout(200);

        const reasoningSelects = page.locator('.reasoning-effort-select');
        const count = await reasoningSelects.count();

        if (count > 0) {
            // Change first model's reasoning effort to 'high'
            await reasoningSelects.first().selectOption({ value: 'high' });

            // Save
            await page.click('button:has-text("Save")');
            await page.waitForTimeout(1000);

            // Close modal if still open
            const closeBtn = page.locator('button:has-text("Cancel"), .close-btn').first();
            if (await closeBtn.isVisible()) {
                await closeBtn.click();
                await page.waitForTimeout(300);
            }

            // Reopen settings
            await page.click('[aria-label="Settings"]');
            await page.waitForSelector('[role="dialog"]', { timeout: 5000 });

            // Expand first provider again
            await providerHeader.click();
            await page.waitForTimeout(300);

            // Check that the value persisted
            const savedSelect = page.locator('.reasoning-effort-select').first();
            const savedValue = await savedSelect.inputValue();
            expect(savedValue).toBe('high');

            // Change back to medium and save to restore
            await savedSelect.selectOption({ value: 'medium' });
            await page.click('button:has-text("Save")');
            await page.waitForTimeout(500);

            // Close settings
            const closeBtn2 = page.locator('button:has-text("Cancel"), .close-btn').first();
            if (await closeBtn2.isVisible()) {
                await closeBtn2.click();
            }
        }
    });
});
