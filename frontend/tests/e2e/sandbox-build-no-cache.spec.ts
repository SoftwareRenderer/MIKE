import { test, expect, type Browser } from '@playwright/test';
import { writeFileSync, rmSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { csrfFetch } from './helpers';

// "Disable cache" checkbox next to the sandbox image Rebuild button.
// Unticked, the build request carries no no_cache param; ticked, it sends
// ?no_cache=1 (the server then runs `podman build --no-cache`). It is never
// persisted, so the request URL is the observable fact. The build endpoint is
// stubbed, so no podman, gVisor or real image build is needed.
//
// Isolation: dedicated repo + project (/tmp/kanban-test-repo-buildcache) so the
// sandbox_dockerfile_path writes cannot race with other specs on the shared repo.

const REPO = '/tmp/kanban-test-repo-buildcache';
const PROJECT_NAME = 'kanban-test-repo-buildcache';
const DOCKERFILE = join(REPO, 'Dockerfile');

// page fixtures aren't available in beforeAll/afterAll — use a logged-in context.
async function setupPage(browser: Browser) {
  const context = await browser.newContext({
    storageState: '/tmp/kanban-test-storage-state.json',
  });
  const page = await context.newPage();
  await page.goto('/');
  return { context, page };
}

/**
 * Intercept the sandbox build endpoint and record every requested URL.
 * The reply is a valid SSE stream so the modal behaves as in production.
 */
async function stubBuildEndpoint(page: any): Promise<string[]> {
  const urls: string[] = [];
  await page.route('**/api/projects/*/sandbox/build*', async (route: any) => {
    urls.push(route.request().url());
    await route.fulfill({
      status: 200,
      headers: { 'Content-Type': 'text/event-stream' },
      body:
        'data: Building sandbox image from Dockerfile...\n\n' +
        'data: {"status":"built","image":"sandbox-buildcache","project":"p"}\n\n',
    });
  });
  return urls;
}

test.describe.serial('Sandbox build disable-cache option', () => {
  let projectId = '';

  test.beforeAll(async ({ browser }) => {
    rmSync(REPO, { recursive: true, force: true });
    mkdirSync(REPO, { recursive: true });
    writeFileSync(join(REPO, 'README.md'), 'buildcache fixture repo\n');
    writeFileSync(DOCKERFILE, 'FROM alpine:latest\n');
    execSync('git init', { cwd: REPO, stdio: 'ignore' });
    execSync('git add -A', { cwd: REPO, stdio: 'ignore' });
    execSync('git -c user.email="test@kanban.local" -c user.name="Test" commit -m "Initial commit"', {
      cwd: REPO,
      stdio: 'ignore',
    });

    const { context, page } = await setupPage(browser);
    try {
      const res = await csrfFetch(page, '/api/projects', { method: 'POST', body: { path: REPO } });
      if (!res.ok) throw new Error(`create buildcache project: ${JSON.stringify(res.data)}`);
      projectId = res.data.id;
    } finally {
      await context.close();
    }
  });

  test.afterAll(async ({ browser }) => {
    const { context, page } = await setupPage(browser);
    try {
      if (projectId) {
        await csrfFetch(page, `/api/projects/${projectId}`, { method: 'DELETE' });
      }
    } finally {
      await context.close();
    }
    rmSync(REPO, { recursive: true, force: true });
  });

  test.beforeEach(async ({ page }) => {
    // The Dockerfile section — and with it the Rebuild button and the cache
    // checkbox — only renders for a sandboxed project with a SAVED path; an
    // unsaved selection shows "Build" instead.
    const res = await csrfFetch(page, `/api/projects/${projectId}`, {
      method: 'PUT',
      body: { sandbox_enabled: true, sandbox_network_isolation: false, sandbox_dockerfile_path: DOCKERFILE },
    });
    if (!res.ok) throw new Error(`configure sandbox dockerfile: ${JSON.stringify(res.data)}`);
    await expect(async () => {
      const p = await page.evaluate(async (pid) => {
        const r = await fetch(`/api/projects/${pid}`);
        return r.ok ? r.json() : null;
      }, projectId);
      expect(p?.sandbox_dockerfile_path).toBe(DOCKERFILE);
    }).toPass({ timeout: 5000 });

    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.locator('.project-item', { hasText: PROJECT_NAME }).first().click();
  });

  async function openSandboxConfig(page: any) {
    await page.click('.sandbox-config-btn');
    await expect(page.locator('.config-options')).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.dockerfile-path-text')).toHaveText(DOCKERFILE, { timeout: 5000 });
  }

  test('shows an unchecked "Disable cache" checkbox beside Rebuild and defaults to the cache', async ({ page }) => {
    const requested = await stubBuildEndpoint(page);
    await openSandboxConfig(page);

    const box = page.locator('input[aria-label="Disable build cache"]');
    await expect(box).toBeVisible({ timeout: 5000 });
    await expect(box).not.toBeChecked();
    await expect(box).toBeEnabled();

    // Inline: same action group as the rebuild button it modifies
    await expect(box.locator('xpath=ancestor::div[contains(@class,"build-actions")]//button[contains(@class,"btn-rebuild")][1]')).toBeVisible();

    await page.locator('.btn-rebuild').click();
    await expect.poll(() => requested.length).toBe(1);
    expect(requested[0]).not.toContain('no_cache');
  });

  test('ticking "Disable cache" requests a cache-bypassing build', async ({ page }) => {
    const requested = await stubBuildEndpoint(page);
    await openSandboxConfig(page);

    const box = page.locator('input[aria-label="Disable build cache"]');
    await expect(box).toBeVisible({ timeout: 5000 });
    await box.check();
    await expect(box).toBeChecked();

    await page.locator('.btn-rebuild').click();
    await expect.poll(() => requested.length).toBe(1);
    expect(requested[0]).toContain('no_cache=1');
  });

  test('resets to unchecked when the modal is reopened (never persisted)', async ({ page }) => {
    const requested = await stubBuildEndpoint(page);
    await openSandboxConfig(page);

    const box = page.locator('input[aria-label="Disable build cache"]');
    await expect(box).toBeVisible({ timeout: 5000 });
    await box.check();
    await page.locator('.close-btn').first().click();
    await expect(page.locator('.config-options')).toBeHidden({ timeout: 5000 });

    await openSandboxConfig(page);
    await expect(page.locator('input[aria-label="Disable build cache"]')).not.toBeChecked();

    // And the fresh modal still builds with the cache by default
    await page.locator('.btn-rebuild').click();
    await expect.poll(() => requested.length).toBe(1);
    expect(requested[0]).not.toContain('no_cache');

    // Nothing was persisted on the project either
    const project = await page.evaluate(async (pid) => {
      const res = await fetch(`/api/projects/${pid}`);
      return res.ok ? res.json() : null;
    }, projectId);
    expect(JSON.stringify(project)).not.toContain('no_cache');
  });
});
