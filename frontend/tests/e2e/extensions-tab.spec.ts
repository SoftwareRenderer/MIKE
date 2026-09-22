import { test, expect, type Page } from '@playwright/test';
import { identityKey } from '../../src/lib/utils/extension-identity';

test.describe.configure({ mode: 'parallel' });

// ============================================================
// Extensions Tab
// ============================================================

// Compute the Browse view's segments from the (fixture-pinned) APIs:
//  1. unpublished — present in the local extensions dir but absent from the
//     registry index (API order); must render first.
//  2. featured — registry entries with featured: true that are not yet
//     installed; must render second (pinned to the top of the registry
//     section).
//  3. rest — the remaining registry entries in API order.
// Also returns the card counts the DOM must reach before ordering assertions
// are safe (local + registry entries load asynchronously, so the rendered
// card set can be transiently partial).
async function getBrowseSegments(page: Page) {
  // The lists are fetched in the page, but the identity model is imported from
  // src/lib/utils/extension-identity — the same module ExtensionsTab.svelte uses.
  // A local copy here drifted (it did not slugify the author, so the registry's
  // "Microsoft/typescript-language-server" and the on-disk
  // "microsoft/typescript-language-server" counted as two extensions) and the
  // Browse assertions were off by one card.
  const lists = await page.evaluate(async () => {
    const j = async (url: string) => {
      const res = await fetch(url);
      return res.ok ? ((await res.json()) as any[]) : [];
    };
    return {
      local: await j('/api/extensions/local'),
      registry: await j('/api/extensions/registry'),
      installed: await j('/api/extensions'),
    };
  });
  const { local, registry, installed } = lists;
  const key = (e: { author?: string; name: string }) => identityKey(e.author, e.name);
  const displayName = (e: any) => e.display_name || e.name;
  const registryKeys = new Set(registry.map(key));
  const installedKeys = new Set(installed.map(key));
  const unpublished = local.filter((e: any) => !registryKeys.has(key(e))).map(displayName);
  const featuredEntries = registry.filter((e: any) => e.featured === true && !installedKeys.has(key(e)));
  const featured = featuredEntries.map(displayName);
  const featuredKeys = new Set(featuredEntries.map(key));
  const rest = registry.filter((e: any) => !featuredKeys.has(key(e))).map(displayName);
  return {
    unpublished,
    featured,
    rest,
    localCount: unpublished.length,
    registryCount: registry.length,
  };
}

// A card is addressed by `data-identity`, the author+name key the app itself compares on. A display
// name is NOT unique in this list: the installed directory is per-machine and gitignored, so a
// developer's own install can display the same name as a fixture entry while being a different
// extension (different author), and both render as their own card.
const browseCard = (page: Page, author: string, name: string) =>
  page.locator(`.registry-card[data-identity="${identityKey(author, name)}"]`);
const installedCard = (page: Page, author: string, name: string) =>
  page.locator(`.extension-card[data-identity="${identityKey(author, name)}"]`);

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
      await expect(page.locator('.empty-state')).toBeVisible({ timeout: 3000 });
    }
  });

  test('should switch between Installed and Browse views', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    await page.click('.view-tab:has-text("Browse")');

    // Should show registry URL input or browse view
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });
  });

  test('should switch to Installed tab when Check for Updates is clicked in Browse view', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

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

  test('should order Browse as local, then featured, then remaining registry extensions', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    const segments = await getBrowseSegments(page);

    // The test is only meaningful with at least one unpublished extension
    // (the e2e servers seed local-extension fixtures in global-setup).
    expect(segments.unpublished.length).toBeGreaterThan(0);
    expect(segments.featured.length).toBeGreaterThan(0);
    expect(segments.rest.length).toBeGreaterThan(0);

    // Switch to Browse
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Wait for the FULL list to render: registry and local entries load
    // asynchronously, so the card set can be transiently partial. The total
    // comes from the API segments above, not the DOM.
    await expect(page.locator('.registry-card')).toHaveCount(
      segments.unpublished.length + segments.featured.length + segments.rest.length,
      { timeout: 10000 },
    );

    // The full card order must be exactly: local, featured, rest.
    const cardNames = await page
      .locator('.registry-card .ext-name')
      .evaluateAll((els) => els.map((e) => (e.textContent || '').trim()));
    expect(cardNames).toEqual([
      ...segments.unpublished,
      ...segments.featured,
      ...segments.rest,
    ]);

    // Local cards should carry the prominent unpublished styling + badge.
    const unpublishedCards = page.locator('.registry-card.unpublished-card');
    await expect(unpublishedCards).toHaveCount(segments.unpublished.length);
    await expect(unpublishedCards.first().locator('.unpublished-badge')).toHaveText('Local');

    // The published (registry) card must not be styled as unpublished. The registry fixture's own
    // author+name, so a same-named local install on the developer's disk cannot answer for it.
    const tsCard = browseCard(page, 'Microsoft', 'typescript-language-server');
    await expect(tsCard.locator('.ext-name')).toHaveText('TypeScript/JavaScript Language Server');
    await expect(tsCard).not.toHaveClass(/unpublished-card/);
    await expect(tsCard.locator('.unpublished-badge')).toHaveCount(0);
  });

  test('should pin a featured (not installed) registry extension to the top of the registry section with a Featured badge', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    const { featured, localCount, registryCount } = await getBrowseSegments(page);
    expect(featured.length).toBeGreaterThan(0);

    // Switch to Browse
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Wait for the fully-rendered list (local + registry entries).
    await expect(page.locator('.registry-card')).toHaveCount(localCount + registryCount, {
      timeout: 10000,
    });

    // The featured entry renders above every non-featured registry entry:
    // it is the first registry card after the local (unpublished) block.
    const featuredCard = page.locator('.registry-card').nth(localCount);
    await expect(featuredCard.locator('.ext-name')).toHaveText(featured[0]);
    await expect(featuredCard.locator('.featured-badge')).toHaveText('Featured');

    // No other card carries the Featured badge (non-featured registry
    // entries and local cards do not).
    await expect(page.locator('.registry-card .featured-badge')).toHaveCount(featured.length);

    // The featured entry is not installed, so it still offers Install.
    await expect(featuredCard.locator('button:has-text("Install")')).toBeVisible();
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
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });

    // Click Install on a local (unpublished) card: the seeded fixture, named by its manifest author.
    const svelteCard = browseCard(page, 'Svelte Contributors', 'svelte-language-server');
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

    // Go to Browse and install the local "Light Mode" extension.
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });
    const lightCard = browseCard(page, 'SoftwareRenderer', 'mike-light');
    await expect(lightCard).toBeVisible({ timeout: 10000 });
    await lightCard.locator('button:has-text("Install")').click();

    // Wait for it to be marked installed in the browse list.
    await expect(lightCard.locator('.installed-badge')).toBeVisible({ timeout: 10000 });

    // Switch to Installed and verify the local card carries the same styling
    // (hazard-stripe card + "Local" badge).
    await page.click('.view-tab:has-text("Installed")');
    const installedLight = installedCard(page, 'SoftwareRenderer', 'mike-light');
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

    // Switch to Browse view
    await page.click('.view-tab:has-text("Browse")');
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
