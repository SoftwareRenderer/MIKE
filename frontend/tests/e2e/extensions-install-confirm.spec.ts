import { test, expect, type Page } from '@playwright/test';

/**
 * Install confirm dialog — Settings → Extensions (Browse view).
 *
 * Installing an extension that declares the network capability or
 * sandbox-escaping permissions (network.proxy, preview.serveScripts,
 * preview.serveNetwork) must show a confirm dialog listing the network
 * capability first (with its allowed domains) and ALL of its declared
 * permissions (dangerous ones first, with the warning icon) before the
 * install starts; Cancel aborts. Extensions without the network capability
 * and without dangerous permissions install without the dialog.
 *
 * Uses local (unpublished) extensions: github-issue-importer declares the
 * network capability (allowed domain api.github.com), network.proxy
 * (dangerous) plus two safe createTask permissions, and markdown-preview
 * declares only safe editor permissions.
 *
 * Runs in the `isolated` project: installing extensions mutates server
 * state, and the suite is serialized to avoid races on the shared DB (cf.
 * extensions-danger-permissions.spec.ts, which installs html-preview).
 */

const STORAGE_STATE = '/tmp/kanban-test-storage-state.json.isolated';

async function openExtensionsBrowse(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.project-item', { timeout: 10000 });
  await page.click('.icon-btn[aria-label="Settings"]');
  await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
  await page.click('text=Extensions');
  await page.waitForSelector('.extensions-tab', { timeout: 5000 });
  await page.click('.view-tab:has-text("Browse")');
  await page.waitForSelector('.browse-view', { timeout: 5000 });
}

test.describe.serial('Install confirm dialog in Extensions browse view', () => {
  test('shows a permission confirm dialog before installing an extension with dangerous permissions', async ({ page }) => {
    await openExtensionsBrowse(page);
    const card = page.locator('.extension-card', { hasText: 'GitHub Issue Importer' });
    await expect(card).toBeVisible({ timeout: 10000 });

    // First attempt: the dialog appears instead of the install starting.
    await card.locator('button:has-text("Install")').click();
    const dialog = page.locator('.install-confirm-modal');
    await expect(dialog).toBeVisible({ timeout: 5000 });
    await expect(dialog.locator('.modal-header h2')).toHaveText('Install "GitHub Issue Importer"?');
    await expect(dialog.locator('.install-confirm-intro')).toBeVisible();

    // The network capability is listed first, with the warning icon (its
    // <title> text is part of the rendered text, so normalize before
    // comparing) and its allowed domains as tags.
    await expect(dialog.locator('.cap-permission')).toHaveCount(4);
    const names = dialog.locator('.cap-permission-name');
    const norm = (s: string) => s.replace(/\s+/g, ' ').trim();
    const labels = (await names.allTextContents()).map(norm);
    expect(labels[0]).toContain('Network');
    expect(labels[0]).toContain('Potentially dangerous');
    expect(await dialog.locator('.cap-detail-tag')).toHaveCount(1);
    await expect(dialog.locator('.cap-detail-tag')).toHaveText('api.github.com');
    // Then the declared permissions: the dangerous one first (warning icon),
    // the safe ones follow in manifest order.
    expect(labels[1]).toContain('Reach external services');
    expect(labels[1]).toContain('Potentially dangerous');
    expect(labels[2]).toBe('Edit the task description');
    expect(labels[3]).toBe('See what you paste');
    expect(await names.nth(0).locator('svg.perm-danger-icon').count()).toBe(1);
    expect(await names.nth(1).locator('svg.perm-danger-icon').count()).toBe(1);
    expect(await names.nth(2).locator('svg.perm-danger-icon').count()).toBe(0);
    expect(await names.nth(3).locator('svg.perm-danger-icon').count()).toBe(0);

    // Cancel aborts: no install started, the card still offers Install.
    await dialog.locator('button:has-text("Cancel")').click();
    await expect(dialog).toBeHidden();
    await expect(card.locator('button:has-text("Install")')).toBeVisible();

    // Second attempt: confirming starts the install and the card flips to
    // Installed.
    await card.locator('button:has-text("Install")').click();
    await expect(page.locator('.install-confirm-modal')).toBeVisible();
    await page.locator('.install-confirm-modal button:has-text("Install")').click();
    await expect(card.locator('.installed-badge')).toHaveText('Installed', { timeout: 15000 });
  });

  test('installs without the confirm dialog when no dangerous permissions are declared', async ({ page }) => {
    await openExtensionsBrowse(page);
    const card = page.locator('.extension-card', { hasText: 'Markdown Preview' });
    await expect(card).toBeVisible({ timeout: 10000 });

    await card.locator('button:has-text("Install")').click();
    await expect(page.locator('.install-confirm-modal')).toHaveCount(0);
    // The install proceeds straight away.
    await expect(card.locator('.installed-badge')).toHaveText('Installed', { timeout: 15000 });
  });

  // No page fixture in afterAll — dedicated logged-in context.
  test.afterAll(async ({ browser }) => {
    // Remove the extensions installed by this suite so the shared test DB is
    // left clean for other specs.
    const context = await browser.newContext({ storageState: STORAGE_STATE });
    const page = await context.newPage();
    try {
      await page.goto('/');
      await page.evaluate(async () => {
        const session = await (await fetch('/api/auth/session')).json();
        const csrf = session.csrf_token || '';
        const listRes = await fetch('/api/extensions');
        if (!listRes.ok) return;
        const list: any[] = await listRes.json();
        for (const ext of list) {
          if (ext.name === 'github-issue-importer' || ext.name === 'markdown-preview') {
            await fetch(`/api/extensions/${ext.id}`, {
              method: 'DELETE',
              headers: { 'X-CSRF-Token': csrf },
              credentials: 'include',
            });
          }
        }
      });
    } finally {
      await context.close();
    }
  });
});
