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

  test('should switch to Installed tab when Check for Updates is clicked in Browse view', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    // Click Extensions tab
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Switch to Browse view
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Click Check for Updates from the Browse tab
    await page.click('button:has-text("Check for Updates")');

    // The view must switch to Installed so the check results are visible
    await expect(page.locator('.installed-view')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.view-tab:has-text("Installed")')).toHaveClass(/active/);
    await expect(page.locator('.browse-view')).toHaveCount(0);

    // The check must complete (button label returns from "Checking..." to "Check for Updates")
    await expect(page.locator('button:has-text("Check for Updates")')).toBeVisible({ timeout: 15000 });
  });

  test('should show unpublished extensions at the top of Browse Registry', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Compute unpublished extensions from the API: present in the local
    // extensions dir but absent from the registry index.
    const unpublished = await page.evaluate(async () => {
      const localRes = await fetch('/api/extensions/local');
      const regRes = await fetch('/api/extensions/registry');
      if (!localRes.ok || !regRes.ok) return [];
      const local = (await localRes.json()) as any[];
      const registry = (await regRes.json()) as any[];
      const registryNames = new Set(registry.map((e: any) => e.name));
      return local
        .filter((e: any) => !registryNames.has(e.name))
        .map((e: any) => e.display_name || e.name);
    });

    // The test is only meaningful when there is at least one unpublished extension.
    expect(unpublished.length).toBeGreaterThan(0);

    // Switch to Browse Registry
    await page.click('text=Browse Registry');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Wait for cards to render
    await expect(page.locator('.registry-card').first()).toBeVisible({ timeout: 10000 });

    // The first cards (in API order) must be exactly the unpublished extensions.
    const cardNames = await page
      .locator('.registry-card .ext-name')
      .evaluateAll((els) => els.map((e) => (e.textContent || '').trim()));
    expect(cardNames.slice(0, unpublished.length)).toEqual(unpublished);

    // The unpublished svelte-language-server should appear before the published
    // TypeScript server (the only entry in the registry fixture).
    const svelteIdx = cardNames.indexOf('Svelte Language Server');
    const tsIdx = cardNames.indexOf('TypeScript/JavaScript Language Server');
    expect(svelteIdx).toBeGreaterThanOrEqual(0);
    expect(tsIdx).toBeGreaterThan(svelteIdx);

    // Local cards should carry the prominent unpublished styling + badge.
    const unpublishedCards = page.locator('.registry-card.unpublished-card');
    await expect(unpublishedCards).toHaveCount(unpublished.length);
    await expect(unpublishedCards.first().locator('.unpublished-badge')).toHaveText('Local');

    // The published (registry) card must not be styled as unpublished.
    const tsCard = page.locator('.registry-card', { hasText: 'TypeScript/JavaScript Language Server' });
    await expect(tsCard).not.toHaveClass(/unpublished-card/);
    await expect(tsCard.locator('.unpublished-badge')).toHaveCount(0);
  });

  test('should install a local extension via the install-local endpoint', async ({ page }) => {
    // Intercept the install-local request so the test avoids a real (slow)
    // container image build while still verifying the routing.
    let installLocalCalled = false;
    await page.route('**/api/extensions/install-local', async (route) => {
      installLocalCalled = true;
      await route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream' },
        body: 'data: ' + JSON.stringify({ extension: 'svelte-language-server', progress: 100, message: 'Installed', done: true, result: {} }) + '\n\n',
      });
    });

    // Open settings > Extensions > Browse Registry
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });
    await page.click('text=Browse Registry');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Click Install on a local (unpublished) card.
    const svelteCard = page.locator('.registry-card', { hasText: 'Svelte Language Server' });
    await expect(svelteCard).toBeVisible({ timeout: 10000 });
    await svelteCard.locator('button:has-text("Install")').click();

    // The request must have gone to the install-local endpoint (not /install).
    await expect.poll(() => installLocalCalled).toBe(true);
  });

  test('should style local extensions in the Installed list', async ({ page }) => {
    // Install a local CSS extension (no Dockerfile, so no image build).
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Go to Browse Registry and install the local "Light Mode" extension.
    await page.click('text=Browse Registry');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });
    const lightCard = page.locator('.registry-card', { hasText: 'Light Mode' });
    await expect(lightCard).toBeVisible({ timeout: 10000 });
    await lightCard.locator('button:has-text("Install")').click();

    // Wait for it to be marked installed in the browse list.
    await expect(lightCard.locator('.installed-badge')).toBeVisible({ timeout: 10000 });

    // Switch to Installed and verify the local card carries the same styling
    // (hazard-stripe card + "Local" badge).
    await page.click('.view-tab:has-text("Installed")');
    const installedLight = page.locator('.extension-card', { hasText: 'Light Mode' });
    await expect(installedLight).toBeVisible({ timeout: 10000 });
    await expect(installedLight).toHaveClass(/unpublished-card/);
    await expect(installedLight.locator('.unpublished-badge')).toHaveText('Local');

    // Local extensions should be listed first (at the top of the Installed list).
    const firstName = await page
      .locator('.extension-card .ext-name')
      .first()
      .textContent();
    expect(firstName).toBe('Light Mode');
  });

  test('should show capability badges in Browse Registry view', async ({ page }) => {
    // Navigate; skip if the (fixture-pinned) registry is unreachable
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    const registryOk = await page.evaluate(async () => {
      const res = await fetch('/api/extensions/registry');
      return res.ok;
    });
    if (!registryOk) {
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

  test('should serve the default registry index', async ({ page }) => {
    const index = await page.evaluate(async () => {
      const res = await fetch('/api/extensions/registry');
      if (!res.ok) return null;
      return res.json();
    });
    expect(Array.isArray(index)).toBe(true);
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
    const index = await page.evaluate(async () => {
      const res = await fetch('/api/extensions/registry');
      if (!res.ok) return null;
      return res.json();
    });
    if (!Array.isArray(index)) {
      test.skip();
    }

    const tsEntry = index.find((entry: any) => entry.name === 'typescript-language-server');
    expect(tsEntry).toBeDefined();
    expect(tsEntry.type).toBe('lsp');
    expect(tsEntry.display_name).toBe('TypeScript/JavaScript Language Server');
  });

  test('should have correct registry entry fields for typescript-language-server', async ({ page }) => {
    const index = await page.evaluate(async () => {
      const res = await fetch('/api/extensions/registry');
      if (!res.ok) return null;
      return res.json();
    });
    if (!Array.isArray(index)) {
      test.skip();
    }

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
