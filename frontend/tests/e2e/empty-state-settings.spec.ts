import { test, expect } from '@playwright/test';

test.describe.serial('Empty state', () => {
  let savedConfig: any = null;

  test('should show welcome message when no providers configured', async ({ page, request }) => {
    // Save current config and temporarily clear providers
    savedConfig = await request.get('http://localhost:3000/api/config').then(r => r.json());
    await request.put('http://localhost:3000/api/config', {
      data: { providers: [] },
    });

    await page.goto('/');
    await page.waitForSelector('.empty-state', { timeout: 10000 });

    await expect(page.getByText('Welcome to Kanban')).toBeVisible();
    await expect(page.getByText(/Configure an LLM provider/)).toBeVisible();
    await expect(page.getByRole('button', { name: 'Configure Provider' })).toBeVisible();

    // Restore providers
    await request.put('http://localhost:3000/api/config', {
      data: {
        providers: savedConfig.providers,
        default_provider: savedConfig.default_provider,
      },
    });
  });

  test('should open config modal from "Configure Provider" button', async ({ page, request }) => {
    savedConfig = await request.get('http://localhost:3000/api/config').then(r => r.json());
    await request.put('http://localhost:3000/api/config', {
      data: { providers: [] },
    });

    await page.goto('/');
    await page.waitForSelector('.empty-state', { timeout: 10000 });

    await page.getByRole('button', { name: 'Configure Provider' }).click();
    await expect(page.getByRole('dialog')).toBeVisible();

    // Restore providers
    await request.put('http://localhost:3000/api/config', {
      data: {
        providers: savedConfig.providers,
        default_provider: savedConfig.default_provider,
      },
    });
  });

  test('should show "No Projects" when providers are configured', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.empty-state', { timeout: 10000 });

    await expect(page.getByText('No Projects')).toBeVisible();
    await expect(page.locator('.empty-state .btn-primary')).toBeVisible();
  });

  test('should show settings button in empty state', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.empty-state', { timeout: 10000 });

    await expect(page.getByRole('button', { name: 'Settings' })).toBeVisible();
  });
});
