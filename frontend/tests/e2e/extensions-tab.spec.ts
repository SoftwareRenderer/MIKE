import { test, expect } from '@playwright/test';

test.describe.configure({ mode: 'parallel' });

// ============================================================
// Extensions Tab
// ============================================================

test.describe('Extensions Tab', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should open Extensions tab from Settings', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    // Click Extensions tab
    await page.click('text=Extensions');

    // Should show extensions tab content
    await expect(page.locator('.extensions-tab')).toBeVisible({ timeout: 5000 });
  });

  test('should show empty state when no extensions installed', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    // Click Extensions tab
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Should show installed count — check against actual API count to avoid
    // flakiness from parallel tests creating extensions.
    const actualCount = await page.evaluate(async () => {
      const res = await fetch('/api/extensions');
      return res.ok ? res.json().then((exts: any[]) => exts.length) : 0;
    });
    const installedTab = await page.locator('.view-tab:has-text("Installed")').first().textContent();
    expect(installedTab).toContain(String(actualCount));
    if (actualCount === 0) {
      // Verify empty state when truly no extensions installed
      await expect(page.locator('.extensions-empty, .ext-empty-state')).toBeVisible({ timeout: 3000 });
    }
  });

  test('should switch between Installed and Browse views', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    // Click Extensions tab
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Click Browse Registry tab
    await page.click('text=Browse Registry');

    // Should show registry URL input or browse view
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });
  });

  test('should show capability badges in Browse Registry view', async ({ page }) => {
    // Navigate and check registry URL is configured
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });
    if (!config.registry_url) {
      test.skip();
    }

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Switch to Browse Registry view
    await page.click('text=Browse Registry');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Wait for registry cards to load
    await expect(page.locator('.registry-card').first()).toBeVisible({ timeout: 10000 });

    // Check that registry cards show capability badges
    const firstCard = page.locator('.registry-card').first();
    const badges = firstCard.locator('.cap-badge');
    await expect(badges).toHaveCount(1);

    // Verify the badge has an aria-label for accessibility
    const ariaLabel = await badges.first().getAttribute('aria-label');
    expect(ariaLabel).toBeTruthy();
    expect(ariaLabel!.length).toBeGreaterThan(0);
  });
});

// ============================================================
// Registry URL Config
// ============================================================

test.describe('Registry URL Config', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should have default registry URL', async ({ page }) => {
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });

    expect(config.registry_url).toBeDefined();
    expect(typeof config.registry_url).toBe('string');
  });
});

// ============================================================
// TypeScript Language Server Extension
// ============================================================

test.describe('TypeScript Language Server Extension', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should list typescript-language-server in registry', async ({ page }) => {
    // Fetch registry via API
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });

    const registryUrl = config.registry_url;
    if (!registryUrl) {
      test.skip();
    }

    const index = await page.evaluate(async (url) => {
      const res = await fetch(`/api/extensions/registry?url=${encodeURIComponent(url)}`);
      return res.json();
    }, registryUrl);

    expect(Array.isArray(index)).toBe(true);

    const tsEntry = index.find((entry: any) => entry.name === 'typescript-language-server');
    expect(tsEntry).toBeDefined();
    expect(tsEntry.type).toBe('lsp');
    expect(tsEntry.display_name).toBe('TypeScript/JavaScript Language Server');
  });

  test('should have correct registry entry fields for typescript-language-server', async ({ page }) => {
    const config = await page.evaluate(async () => {
      const res = await fetch('/api/config');
      return res.json();
    });

    const registryUrl = config.registry_url;
    if (!registryUrl) {
      test.skip();
    }

    const index = await page.evaluate(async (url) => {
      const res = await fetch(`/api/extensions/registry?url=${encodeURIComponent(url)}`);
      return res.json();
    }, registryUrl);

    const tsEntry = index.find((entry: any) => entry.name === 'typescript-language-server');
    expect(tsEntry).toBeDefined();
    expect(tsEntry.type).toBe('lsp');
    // Git-pinned source fields.
    expect(tsEntry.author).toBe('Microsoft');
    expect(tsEntry.subdir).toBe('lsp/typescript-language-server');
    expect(typeof tsEntry.commit).toBe('string');
    expect(tsEntry.repo).toContain('SoftwareRenderer/MIKE-extensions');
  });
});
