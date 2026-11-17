import { test, expect, type Page } from '@playwright/test';
import { identityKey } from '../../src/lib/utils/extension-identity';
import { apiGetJson, csrfFetch, openSettingsTab } from './helpers';

// ============================================================
// Extensions Tab — Settings → Extensions, and the CSS extension it installs
// ============================================================
//
// One file because one install drives both halves: the Browse/Installed list is how a CSS extension
// gets onto the server, and the injected <link> is what the install is for. Installing one is server
// state that every other extension spec reads, so this file runs on the `isolated` lane, and the
// fixture is installed once in `beforeAll` and removed in `afterAll` rather than per test.

// Compute the Browse view's segments from the (fixture-pinned) APIs:
//  1. unpublished — present in the local extensions dir but absent from the
//     registry index (API order); must render first.
//  2. featured — registry entries with featured: true that are not yet
//     installed; must render second (pinned to the top of the registry
//     section).
//  3. the registry's translation pack for the browser's own language, when it
//     is not installed; must render third. The suite's default locale is the
//     fallback (`en`), so it promotes nothing and these two segments are the
//     whole list; the tier itself is `extensions-browse-locale-order.unit.svelte.ts`.
//  4. rest — the remaining registry entries in API order.
// Also returns the card counts the DOM must reach before ordering assertions
// are safe (local + registry entries load asynchronously, so the rendered
// card set can be transiently partial).
async function getBrowseSegments(page: Page) {
  // The identity model is imported from src/lib/utils/extension-identity, the module
  // ExtensionsTab.svelte uses, so the test's idea of "the same extension" cannot drift from the app's.
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

async function openExtensionsTab(page: Page) {
  await openSettingsTab(page, 'Extensions');
  await page.waitForSelector('.extensions-tab', { timeout: 5000 });
}

// ── the CSS fixture both halves below install ────────────────────────
//
// Driven the way a user does it (install the local fixture, toggle it), because there is no
// "create extension" API. The fixture (tests/fixtures/local-extensions/.../mike-light) is copied
// into the server's extensions dir by global-setup.

const FIXTURE = { author: 'softwarerenderer', name: 'mike-light' };
const CSS_HREF = `/extensions/${FIXTURE.author}/${FIXTURE.name}/styles.css`;

/** The injected <link> for the fixture extension, whether or not it is there yet. */
function cssLink(page: Page) {
  return page.locator(`link[rel="stylesheet"][href*="${FIXTURE.name}"]`);
}

async function installedId(page: Page): Promise<string> {
  const list = await apiGetJson<any[]>(page, '/api/extensions');
  return list.find((e: any) => identityKey(e.author, e.name) === identityKey(FIXTURE.author, FIXTURE.name))?.id || '';
}

async function setEnabled(page: Page, enabled: boolean) {
  const id = await installedId(page);
  expect(id).not.toBe('');
  const res = await csrfFetch(page, `/api/extensions/${id}/enabled`, { method: 'PATCH', body: { enabled } });
  expect(res.ok).toBe(true);
}

test.describe.serial('Extensions Tab', () => {
  test.beforeAll(async ({ browser }) => {
    // A leftover install from an interrupted run would leave the Browse card showing "Installed" instead
    // of the Install button the first test clicks, and the stylesheet tests below read the enabled flag
    // the install left behind. Start from nothing installed.
    const page = await browser.newPage();
    const id = await installedId(page);
    if (id) await csrfFetch(page, `/api/extensions/${id}`, { method: 'DELETE' });
    await page.close();
  });

  test.afterAll(async ({ browser }) => {
    // Deleting the row deletes the installed directory; an enabled stylesheet would also theme the app
    // for the next file on this lane's server.
    const page = await browser.newPage();
    const id = await installedId(page);
    if (id) await csrfFetch(page, `/api/extensions/${id}`, { method: 'DELETE' });
    await page.close();
  });

  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('installs a local CSS extension from the Browse list', async ({ page }) => {
    await openExtensionsTab(page);

    // Go to Browse and install the local "Light Mode" extension — a directory with no record is listed
    // as installable under the manifest it shipped with, and there is no "create extension" API.
    await page.click('.view-tab:has-text("Browse")');
    await expect(page.locator('.browse-view')).toBeVisible({ timeout: 5000 });
    const lightCard = browseCard(page, FIXTURE.author, FIXTURE.name);
    await expect(lightCard).toBeVisible({ timeout: 10000 });
    await lightCard.locator('button:has-text("Install")').click();

    // Wait for it to be marked installed in the browse list.
    await expect(lightCard.locator('.installed-badge')).toBeVisible({ timeout: 10000 });
  });

  test('lists an installed local extension first, styled as local', async ({ page }) => {
    await openExtensionsTab(page);

    // The install just made is the same record the Installed view reads, and a local install carries the
    // same styling there as in Browse (hazard-stripe card + "Local" badge).
    await page.click('.view-tab:has-text("Installed")');
    const installedLight = installedCard(page, FIXTURE.author, FIXTURE.name);
    await expect(installedLight).toBeVisible({ timeout: 10000 });
    await expect(installedLight).toHaveClass(/unpublished-card/);
    await expect(installedLight.locator('.unpublished-badge')).toHaveText('Local');

    // Local extensions are listed first (at the top of the Installed list): the local cards form a prefix,
    // so no non-local row sits above a local one, and this install is inside that prefix. Stated over the
    // whole list rather than as "the first card is this fixture's", which held only while this install was
    // the only local one the lane server held — the other extension specs install theirs too.
    const fixtureIdentity = identityKey(FIXTURE.author, FIXTURE.name);
    const rows = await page.locator('.extension-card').evaluateAll((els) =>
      els.map((e) => ({ identity: e.getAttribute('data-identity'), local: e.classList.contains('unpublished-card') })),
    );
    expect(rows.length).toBeGreaterThan(0);
    const firstNonLocal = rows.findIndex((r) => !r.local);
    const lastLocal = rows.map((r) => r.local).lastIndexOf(true);
    expect(lastLocal, `a local install sits below a registry one: ${JSON.stringify(rows)}`)
      .toBeLessThan(firstNonLocal === -1 ? rows.length : firstNonLocal);
    const fixtureIndex = rows.findIndex((r) => r.identity === fixtureIdentity);
    expect(fixtureIndex, `the installed fixture is not in the list: ${JSON.stringify(rows)}`).toBeGreaterThanOrEqual(0);
    expect(fixtureIndex, `the installed fixture is not in the leading local block: ${JSON.stringify(rows)}`)
      .toBeLessThan(firstNonLocal === -1 ? rows.length : firstNonLocal);
  });

  test('injects the installed extension stylesheet and the server serves it', async ({ page }) => {
    await expect(cssLink(page)).toBeAttached({ timeout: 10000 });
    const href = await cssLink(page).getAttribute('href');
    expect(href).toContain(CSS_HREF);

    // The href being in the DOM proves nothing about the file: fetch it, or a 404
    // stylesheet would look like a working theme toggle to every assertion below.
    const res = await page.request.get(href!);
    expect(res.ok()).toBe(true);
    expect(await res.text()).toContain(':root');
  });

  test('disabling the extension removes the injected stylesheet', async ({ page }) => {
    await setEnabled(page, false);

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await expect(cssLink(page)).toHaveCount(0);

    await setEnabled(page, true);
    await page.reload();
    await expect(cssLink(page)).toBeAttached({ timeout: 10000 });
  });

  test('orders Browse as local, then featured, then remaining registry extensions', async ({ page }) => {
    await openExtensionsTab(page);

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

    // The featured entry renders above every non-featured registry entry: it is the first registry
    // card after the local (unpublished) block, it says so, and it still offers Install.
    const featuredCard = page.locator('.registry-card').nth(segments.localCount);
    await expect(featuredCard.locator('.ext-name')).toHaveText(segments.featured[0]);
    await expect(featuredCard.locator('.featured-badge')).toHaveText('Featured');
    await expect(featuredCard.locator('button:has-text("Install")')).toBeVisible();
    // No other card carries the Featured badge (non-featured registry entries and local cards do not).
    await expect(page.locator('.registry-card .featured-badge')).toHaveCount(segments.featured.length);
  });

  test('switches to the Installed view when Check for Updates is clicked in Browse view', async ({ page }) => {
    await openExtensionsTab(page);

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

  test('shows capability badges in Browse Registry view', async ({ page }) => {
    // The registry fixture is served from a file setup wrote (`MIKE_REGISTRY_URL` in global setup), so
    // the route answering is not a favour the network grants this run.
    const registryOk = await page.evaluate(async () => {
      const res = await fetch('/api/extensions/registry');
      return res.ok;
    });
    expect(registryOk, '/api/extensions/registry did not answer — the harness serves the fixture setup wrote').toBe(true);

    await openExtensionsTab(page);

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
