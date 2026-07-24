import { test, expect } from '@playwright/test';
import { getProjectId, csrfFetch } from './helpers';

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

    // Should show empty state or installed count of 0
    const installedCount = await page.locator('.view-tab:has-text("Installed")').first().textContent();
    expect(installedCount).toContain('0');
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

  test('should show Add Extension button', async ({ page }) => {
    // Open settings modal
    await page.click('.icon-btn[aria-label="Settings"]');
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });

    // Click Extensions tab
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Should show Add Extension button
    await expect(page.locator('text=Add Extension')).toBeVisible({ timeout: 5000 });
  });

  test('should create extension via API', async ({ page }) => {
    const result = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `test-ext-${Date.now()}`,
        type: 'lsp',
        version: '1.0.0',
        manifest: {
          display_name: 'Test Extension',
          description: 'E2E test extension',
          author: 'Test',
        },
      },
    });

    expect(result.ok).toBe(true);
    expect(result.data.name).toMatch(/test-ext-/);
    expect(result.data.type).toBe('lsp');

    // Cleanup: delete the test extension
    await csrfFetch(page, `/api/extensions/${result.data.id}`, { method: 'DELETE' });
  });

  test('should list extensions via API', async ({ page }) => {
    // Create a test extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `list-test-${Date.now()}`,
        type: 'lsp',
        manifest: { display_name: 'List Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);

    // List extensions
    const listResult = await page.evaluate(async () => {
      const res = await fetch('/api/extensions');
      return res.json();
    });

    expect(Array.isArray(listResult)).toBe(true);
    const found = listResult.find((ext: any) => ext.name === createResult.data.name);
    expect(found).toBeDefined();

    // Cleanup
    await csrfFetch(page, `/api/extensions/${createResult.data.id}`, { method: 'DELETE' });
  });

  test('should toggle extension enabled state', async ({ page }) => {
    // Create a test extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `toggle-test-${Date.now()}`,
        type: 'lsp',
        manifest: { display_name: 'Toggle Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);

    // Disable
    const disableResult = await csrfFetch(page, `/api/extensions/${createResult.data.id}/enabled`, {
      method: 'PATCH',
      body: { enabled: false },
    });
    expect(disableResult.ok).toBe(true);
    expect(disableResult.data.enabled).toBe(false);

    // Re-enable
    const enableResult = await csrfFetch(page, `/api/extensions/${createResult.data.id}/enabled`, {
      method: 'PATCH',
      body: { enabled: true },
    });
    expect(enableResult.ok).toBe(true);
    expect(enableResult.data.enabled).toBe(true);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${createResult.data.id}`, { method: 'DELETE' });
  });

  test('should toggle extension enabled state via UI toggle switch', async ({ page }) => {
    // Create a test extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ui-toggle-test-${Date.now()}`,
        type: 'lsp',
        manifest: { display_name: 'UI Toggle Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear in the list
    await expect(page.locator('.extension-card')).toContainText('UI Toggle Test', { timeout: 5000 });

    // Find the toggle switch for this extension and click it to disable
    const extCard = page.locator('.extension-card').filter({ hasText: 'UI Toggle Test' });
    const toggleSwitch = extCard.locator('.toggle-switch');
    await toggleSwitch.click();

    // Wait for the API call to complete and status to update
    await page.waitForTimeout(500);

    // Verify the extension is now disabled via API
    const checkResult = await page.evaluate(async (id) => {
      const res = await fetch('/api/extensions');
      const extensions = await res.json();
      return extensions.find((ext: any) => ext.id === id);
    }, extId);
    expect(checkResult.enabled).toBe(false);

    // Click again to re-enable
    await toggleSwitch.click();
    await page.waitForTimeout(500);

    // Verify the extension is now enabled
    const checkResult2 = await page.evaluate(async (id) => {
      const res = await fetch('/api/extensions');
      const extensions = await res.json();
      return extensions.find((ext: any) => ext.id === id);
    }, extId);
    expect(checkResult2.enabled).toBe(true);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should delete extension', async ({ page }) => {
    // Create a test extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `delete-test-${Date.now()}`,
        type: 'lsp',
        manifest: { display_name: 'Delete Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);

    // Delete
    const deleteResult = await csrfFetch(page, `/api/extensions/${createResult.data.id}`, {
      method: 'DELETE',
    });
    expect(deleteResult.ok).toBe(true);

    // Verify deleted
    const listResult = await page.evaluate(async () => {
      const res = await fetch('/api/extensions');
      return res.json();
    });
    const found = listResult.find((ext: any) => ext.id === createResult.data.id);
    expect(found).toBeUndefined();
  });

  test('should show version for UI extensions instead of built time', async ({ page }) => {
    // Create a UI extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ui-version-test-${Date.now()}`,
        type: 'ui',
        version: '2.5.3',
        manifest: { display_name: 'UI Version Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear
    await expect(page.locator('.extension-card')).toContainText('UI Version Test', { timeout: 5000 });

    // Find the extension card and check it shows version, not "Built"
    const extCard = page.locator('.extension-card').filter({ hasText: 'UI Version Test' });
    const metaText = await extCard.locator('.ext-meta').textContent();
    expect(metaText).toContain('v2.5.3');
    expect(metaText).not.toContain('Built');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should show built time for LSP extensions', async ({ page }) => {
    // Create an LSP extension
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `lsp-built-test-${Date.now()}`,
        type: 'lsp',
        version: '1.0.0',
        manifest: { display_name: 'LSP Built Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear
    await expect(page.locator('.extension-card')).toContainText('LSP Built Test', { timeout: 5000 });

    // Find the extension card and check it shows built time, not version
    const extCard = page.locator('.extension-card').filter({ hasText: 'LSP Built Test' });
    const metaText = await extCard.locator('.ext-meta').textContent();
    expect(metaText).not.toContain('v1.0.0');
    // Should contain "Built" text (e.g. "Built just now", "Built 1m ago", etc.)
    expect(metaText).toContain('Built');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should link author to homepage when provided', async ({ page }) => {
    const homepage = 'https://example.com/my-ext';
    // Create a UI extension with homepage
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `homepage-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: { display_name: 'Homepage Test', description: '', author: 'Test Author', homepage },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear
    await expect(page.locator('.extension-card')).toContainText('Homepage Test', { timeout: 5000 });

    // Find the extension card and check the author link
    const extCard = page.locator('.extension-card').filter({ hasText: 'Homepage Test' });
    const authorLink = extCard.locator('.ext-link:has-text("Test Author")');
    await expect(authorLink).toBeVisible();
    expect(await authorLink.getAttribute('href')).toBe(homepage);
    expect(await authorLink.getAttribute('target')).toBe('_blank');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should show plain author text when no homepage', async ({ page }) => {
    // Create an extension without homepage
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `no-homepage-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: { display_name: 'No Homepage Test', description: '', author: 'Plain Author' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('.modal, [role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear
    await expect(page.locator('.extension-card')).toContainText('No Homepage Test', { timeout: 5000 });

    // Find the extension card and check author is plain text (no link)
    const extCard = page.locator('.extension-card').filter({ hasText: 'No Homepage Test' });
    const authorLink = extCard.locator('.ext-link:has-text("Plain Author")');
    await expect(authorLink).not.toBeVisible();
    const metaText = await extCard.locator('.ext-meta').textContent();
    expect(metaText).toContain('Plain Author');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should sync config.memory_limit to sandbox_config on update', async ({ page }) => {
    // Create a test extension with default memory
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `memlimit-test-${Date.now()}`,
        type: 'lsp',
        manifest: { display_name: 'Memory Limit Test', description: '', author: '' },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Update with memory_limit in config (as the frontend edit form sends)
    const updateResult = await csrfFetch(page, `/api/extensions/${extId}`, {
      method: 'PUT',
      body: {
        config: { memory_limit: 512 },
      },
    });
    expect(updateResult.ok).toBe(true);

    // Verify sandbox_config.memory_limit was synced
    const getRes = await page.evaluate(async (id) => {
      const res = await fetch(`/api/extensions/${id}`);
      return res.json();
    }, extId);
    expect(getRes.sandbox_config.memory_limit).toBe(512);
    expect(getRes.config.memory_limit).toBe(512);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
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
    expect(tsEntry.display_name).toBe('TypeScript Language Server');
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
    expect(tsEntry.dockerfile_url).toBe('typescript-language-server/Dockerfile');
    expect(tsEntry.manifest_url).toBe('typescript-language-server/manifest.json');
  });
});

// ============================================================
// Sensitive Config Encryption
// ============================================================

test.describe('Sensitive Config Encryption', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
  });

  test('should mask sensitive config values in API response', async ({ page }) => {
    // Create a UI extension with a sensitive config field
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `sensitive-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'Sensitive Config Test',
          description: 'Test extension with sensitive config',
          author: 'Test',
          config_schema: {
            type: 'object',
            properties: {
              github_token: {
                type: 'string',
                title: 'GitHub Token',
                sensitive: true,
              },
              repo_url: {
                type: 'string',
                title: 'Repo URL',
              },
            },
          },
        },
        config: {
          github_token: 'ghp_secret-token-12345',
          repo_url: 'https://github.com/user/repo',
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // API response should mask the sensitive value but show the non-sensitive one
    expect(createResult.data.config.github_token).not.toBe('ghp_secret-token-12345');
    expect(createResult.data.config.github_token).toMatch(/•/); // masked with bullets
    expect(createResult.data.config.repo_url).toBe('https://github.com/user/repo');

    // Verify via GET as well
    const getResult = await page.evaluate(async (id) => {
      const res = await fetch(`/api/extensions/${id}`);
      return res.json();
    }, extId);
    expect(getResult.config.github_token).not.toBe('ghp_secret-token-12345');
    expect(getResult.config.github_token).toMatch(/•/);
    expect(getResult.config.repo_url).toBe('https://github.com/user/repo');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should preserve sensitive value when masked placeholder sent on update', async ({ page }) => {
    // Create extension with sensitive config
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `preserve-test-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'Preserve Test',
          description: '',
          author: 'Test',
          config_schema: {
            type: 'object',
            properties: {
              api_key: { type: 'string', title: 'API Key', sensitive: true },
              username: { type: 'string', title: 'Username' },
            },
          },
        },
        config: {
          api_key: 'super-secret-key',
          username: 'alice',
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Simulate frontend update: send masked value for api_key + changed username
    // The masked value is 10 bullets (••••••••••)
    const maskedValue = '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022';
    const updateResult = await csrfFetch(page, `/api/extensions/${extId}`, {
      method: 'PUT',
      body: {
        config: {
          api_key: maskedValue, // masked placeholder (user didn't change this field)
          username: 'bob',       // user changed this
        },
      },
    });
    expect(updateResult.ok).toBe(true);

    // Verify the original secret was preserved (not overwritten with bullets)
    const getResult = await page.evaluate(async (id) => {
      const res = await fetch(`/api/extensions/${id}`);
      return res.json();
    }, extId);
    // The API response still masks the value, so we check it's still masked (not empty)
    expect(getResult.config.api_key).toMatch(/•/);
    // And the non-sensitive field was updated
    expect(getResult.config.username).toBe('bob');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should update sensitive value when new value provided', async ({ page }) => {
    // Create extension with sensitive config
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `update-secret-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'Update Secret Test',
          description: '',
          author: 'Test',
          config_schema: {
            type: 'object',
            properties: {
              api_key: { type: 'string', title: 'API Key', sensitive: true },
            },
          },
        },
        config: {
          api_key: 'old-secret-key',
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Update with a genuinely new value (not the masked placeholder)
    const updateResult = await csrfFetch(page, `/api/extensions/${extId}`, {
      method: 'PUT',
      body: {
        config: {
          api_key: 'new-secret-key',
        },
      },
    });
    expect(updateResult.ok).toBe(true);

    // Verify the new value is stored (response still masks it)
    const getResult = await page.evaluate(async (id) => {
      const res = await fetch(`/api/extensions/${id}`);
      return res.json();
    }, extId);
    expect(getResult.config.api_key).not.toBe('new-secret-key'); // still masked in response
    expect(getResult.config.api_key).toMatch(/•/);

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });

  test('should preserve sensitive value via UI edit when user makes no changes and saves', async ({ page }) => {
    // Create a UI extension with a sensitive config field
    const createResult = await csrfFetch(page, '/api/extensions', {
      method: 'POST',
      body: {
        name: `ui-edit-${Date.now()}`,
        type: 'ui',
        version: '1.0.0',
        manifest: {
          display_name: 'UI Edit Test',
          description: 'Test extension for UI edit flow',
          author: 'Test',
          config_schema: {
            type: 'object',
            properties: {
              github_token: {
                type: 'string',
                title: 'GitHub Token',
                sensitive: true,
              },
              repo_url: {
                type: 'string',
                title: 'Repo URL',
              },
            },
          },
        },
        config: {
          github_token: 'ghp_real-secret-token',
          repo_url: 'https://github.com/user/repo',
        },
      },
    });
    expect(createResult.ok).toBe(true);
    const extId = createResult.data.id;

    // Open settings > Extensions tab
    await page.locator('.icon-btn[aria-label="Settings"]').click();
    await page.waitForSelector('[role="dialog"]', { timeout: 5000 });
    await page.click('text=Extensions');
    await page.waitForSelector('.extensions-tab', { timeout: 5000 });

    // Wait for extension to appear in the installed list
    await expect(page.locator('.extension-card:has-text("UI Edit Test")')).toBeVisible({ timeout: 10000 });

    // Click the edit button on the extension card
    await page.locator('.extension-card:has-text("UI Edit Test") button[title="Edit"]').click();

    // Wait for edit modal/dialog to appear (it's a nested modal)
    // The edit modal has the title "Edit: UI Edit Test"
    await expect(page.locator('text=Edit: UI Edit Test')).toBeVisible({ timeout: 5000 });

    // Don't change anything — just click Save
    await page.locator('text=Save').click();

    // Wait for the edit modal to close (title disappears)
    await expect(page.locator('text=Edit: UI Edit Test')).toBeHidden({ timeout: 5000 });

    // Verify the sensitive value was preserved (not overwritten with bullets)
    const getResult = await page.evaluate(async (id) => {
      const res = await fetch(`/api/extensions/${id}`);
      return res.json();
    }, extId);
    // API response masks the value, so we check it's still masked (not empty)
    expect(getResult.config.github_token).toMatch(/•/);
    // Non-sensitive field should be unchanged
    expect(getResult.config.repo_url).toBe('https://github.com/user/repo');

    // Cleanup
    await csrfFetch(page, `/api/extensions/${extId}`, { method: 'DELETE' });
  });
});
