import { test, expect } from '@playwright/test';
import { apiPutConfig, apiGetConfig, waitForConfig } from './helpers';

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
