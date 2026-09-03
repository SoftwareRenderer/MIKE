import { test, expect, type Page } from '@playwright/test';
import { csrfFetch, apiGetJson } from './helpers';

// ============================================================
// CSS extension: installed -> stylesheet injected -> served
// ============================================================
//
// Driven the way a user does it (install the local fixture, toggle it), because there is no
// "create extension" API. The fixture (tests/fixtures/local-extensions/.../mike-light) is copied
// into the server's extensions dir by global-setup. Installing it themes the whole app for
// everyone sharing this server, so afterAll removes it.

const FIXTURE = { author: 'softwarerenderer', name: 'mike-light' };
const CSS_HREF = `/extensions/${FIXTURE.author}/${FIXTURE.name}/styles.css`;

/** The injected <link> for the fixture extension, whether or not it is there yet. */
function cssLink(page: Page) {
  return page.locator(`link[rel="stylesheet"][href*="${FIXTURE.name}"]`);
}

async function installedId(page: Page): Promise<string> {
  const list = await apiGetJson<any[]>(page, '/api/extensions');
  return list.find((e: any) => e.name === FIXTURE.name)?.id || '';
}

async function setEnabled(page: Page, enabled: boolean) {
  const id = await installedId(page);
  expect(id).not.toBe('');
  const res = await csrfFetch(page, `/api/extensions/${id}/enabled`, { method: 'PATCH', body: { enabled } });
  expect(res.ok).toBe(true);
}

test.describe.serial('CSS extension injection', () => {
  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage();
    // Idempotent: the extension survives an interrupted run (and another spec may have
    // installed the same fixture), and install-local refuses an already-installed one.
    if (!(await installedId(page))) {
      const res = await csrfFetch(page, '/api/extensions/install-local', { method: 'POST', body: FIXTURE });
      expect(res.ok, `installing the css fixture failed: ${JSON.stringify(res.data)}`).toBe(true);
    }
    await page.close();
  });

  test.afterAll(async ({ browser }) => {
    const page = await browser.newPage();
    const id = await installedId(page);
    if (id) await csrfFetch(page, `/api/extensions/${id}`, { method: 'DELETE' });
    await page.close();
  });

  test('injects the extension stylesheet and the server serves it', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });

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

  test('the injected stylesheet survives a reload, in addition to the app bundles', async ({ page }) => {
    await page.goto('/');
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.reload();

    await expect(cssLink(page)).toBeAttached({ timeout: 10000 });
    const appCss = page.locator('link[rel="stylesheet"][href*="/assets/"]');
    expect(await appCss.count()).toBeGreaterThan(0);
  });
});
