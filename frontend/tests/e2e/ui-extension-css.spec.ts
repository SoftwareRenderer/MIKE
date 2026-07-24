import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

test.describe.configure({ mode: 'parallel' });

test.describe('UI Extension CSS URL', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.card', { timeout: 10000 });
  });

  test('should create a CSS extension via API', async ({ page }) => {
    const result = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `css-test-${Date.now()}`,
        version: '1.0.0',
        type: 'css',
        manifest: {
          display_name: 'CSS Test Extension',
          description: 'Test CSS URL',
          author: 'Test',
          css: {
            css_url: '/extensions/test/test.css',
          },
        },
      },
    });

    expect(result.ok).toBe(true);
    expect(result.data.manifest.css.css_url).toBe('/extensions/test/test.css');
  });

  test('should inject stylesheet link when CSS extension is enabled', async ({ page }) => {
    // Create CSS extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `css-link-test-${Date.now()}`,
        version: '1.0.0',
        type: 'css',
        manifest: {
          display_name: 'CSS Link Test',
          description: 'Test',
          author: 'Test',
          css: {
            css_url: '/extensions/test/test.css',
          },
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Reload to trigger extension loading
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });

    // Wait a moment for extensions to load
    await page.waitForTimeout(1000);

    // Check that the link tag exists in <head>
    const hasLink = await page.evaluate((href) => {
      const link = document.getElementById('kanban-theme-stylesheet');
      return link ? link.getAttribute('href') === href : false;
    }, '/extensions/test/test.css');
    expect(hasLink).toBe(true);

    // Check localStorage is populated
    const cached = await page.evaluate(() => localStorage.getItem('kanban-theme-css'));
    expect(cached).toBe('/extensions/test/test.css');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should remove stylesheet link when CSS extension is disabled', async ({ page }) => {
    // Create CSS extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `css-toggle-test-${Date.now()}`,
        version: '1.0.0',
        type: 'css',
        manifest: {
          display_name: 'CSS Toggle Test',
          description: 'Test',
          author: 'Test',
          css: {
            css_url: '/extensions/test/test.css',
          },
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Reload to load the extension
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.waitForTimeout(1000);

    // Verify link exists
    let hasLink = await page.evaluate(() => {
      return document.getElementById('kanban-theme-stylesheet') !== null;
    });
    expect(hasLink).toBe(true);

    // Disable the extension
    const disableResult = await csrfFetch(page, `/api/extensions/${extId}/enabled`, {
      method: 'PATCH',
      body: { enabled: false },
    });
    expect(disableResult.ok).toBe(true);

    // Reload to pick up the change
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.waitForTimeout(1000);

    // Verify link is removed
    hasLink = await page.evaluate(() => {
      return document.getElementById('kanban-theme-stylesheet') !== null;
    });
    expect(hasLink).toBe(false);

    // Verify localStorage is cleared
    const cached = await page.evaluate(() => localStorage.getItem('kanban-theme-css'));
    expect(cached).toBeNull();

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should preserve theme across page reload via localStorage', async ({ page }) => {
    // Create CSS extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `css-persist-test-${Date.now()}`,
        version: '1.0.0',
        type: 'css',
        manifest: {
          display_name: 'CSS Persist Test',
          description: 'Test',
          author: 'Test',
          css: {
            css_url: '/extensions/test/persist.css',
          },
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // First load — extension loads, caches CSS URL
    await page.reload();
    await page.waitForSelector('.card', { timeout: 10000 });
    await page.waitForTimeout(1000);

    const cached = await page.evaluate(() => localStorage.getItem('kanban-theme-css'));
    expect(cached).toBe('/extensions/test/persist.css');

    // Second reload — inline script should inject from localStorage before extensions load
    await page.reload();
    await page.waitForTimeout(500); // Short wait — inline script runs synchronously

    const hasLink = await page.evaluate((href) => {
      const link = document.getElementById('kanban-theme-stylesheet');
      return link ? link.getAttribute('href') === href : false;
    }, '/extensions/test/persist.css');
    expect(hasLink).toBe(true);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });
});
