import { test, expect } from '@playwright/test';
import { apiGetJson, csrfFetch } from './helpers';

// ============================================================
// First-run language offer: a zh-CN browser with no pack installed is
// offered the registry's 简体中文 pack, installs it in one click, and the UI
// switches without a reload.
// ============================================================
//
// Everything here is the real path: the registry index comes from the fixture
// (pinned by global-setup to a local git repo it builds), the install is the SSE
// `POST /api/extensions/install` the offer's button calls, and the language switch
// is the `extension_change` broadcast + `loadI18NFromExtensions` that the app
// already runs. Nothing is stubbed, so what fails is the wiring, not a mock.
//
// Runs on a lane server (`ISOLATED_SPECS`) (see ISOLATED_SPECS): installing an extension
// mutates server state.
//
// The observable proof of the live switch is `document.documentElement.lang` —
// `en.ts` holds only the offer's own strings, so there is no translated label to
// point at yet. That is the same measure the manual gate in docs/i18n.md uses.

const PACK = { author: 'testdev', name: 'locale-zh' };
const PACK_KEY = `${PACK.author}/${PACK.name}`;
const CATALOG_PATH = `/extensions/${PACK_KEY}/messages.json`;
const OFFER = '.locale-offer';

/** The pack this registry entry promises. `commit` is resolved by global-setup. */
async function registryPack(page: any) {
    const registry = await apiGetJson<any[]>(page, '/api/extensions/registry');
    return (registry || []).find((e) => e.name === PACK.name);
}

async function installedPack(page: any) {
    const installed = await apiGetJson<any[]>(page, '/api/extensions');
    return (installed || []).find((e) => e.name === PACK.name);
}

/** Delete a pack left behind by an interrupted run, so the offer can appear at all. */
async function removeInstalledPack(page: any): Promise<void> {
    const ext = await installedPack(page);
    if (ext) await csrfFetch(page, `/api/extensions/${ext.id}`, { method: 'DELETE' });
}

test.describe.serial('First-run language offer', () => {
    // Every case starts from "no pack installed", whatever the previous test left
    // behind (an interrupted run included) — the offer is precisely a no-pack
    // state, and a pack left installed would make a later case pass for the wrong
    // reason or fail for someone else's.
    test.beforeEach(async ({ page }) => {
        await removeInstalledPack(page);
    });

    test.afterAll(async ({ browser }) => {
        const page = await browser.newPage();
        await removeInstalledPack(page);
        await page.close();
    });

    test.describe('an English browser', () => {
        // The default lane is en-US, so this is the case every other spec runs in:
        // if an offer ever appears for them, the dismissal logic is wrong.
        test.use({ locale: 'en-US' });

        test('is never offered a language pack', async ({ page }) => {
            const logs: string[] = [];
            page.on('console', (msg) => logs.push(msg.text()));

            // The registry does carry a pack — it is the browser's language that says no.
            await page.goto('/');
            await page.waitForSelector('.project-item', { timeout: 10000 });
            expect(await registryPack(page)).toBeTruthy();

            // Wait for detection to have actually run (a bare `count === 0` would
            // pass before the offer had a chance to appear), then check for chrome.
            await expect
                .poll(() => logs.some((l) => l.includes('no language pack offered (english-browser)')), {
                    timeout: 15000,
                    message: 'detection never reported the English-browser case — the offer never ran or logged nothing',
                })
                .toBe(true);
            await expect(page.locator(OFFER)).toHaveCount(0);
        });
    });

    test.describe('a zh-CN browser', () => {
        test.use({ locale: 'zh-CN' });

        test('is offered the pack, installs it in one click, and switches live', async ({ page }) => {
            // Setup must have pinned the fixture's pack to a commit that exists —
            // otherwise there is nothing to install and every assertion below is
            // about a pack no server can check out.
            await page.goto('/');
            await page.waitForSelector('.project-item', { timeout: 10000 });
            const entry = await registryPack(page);
            expect(entry, 'global-setup did not resolve the i18n pack commit').toBeTruthy();
            expect(entry.commit).toMatch(/^[0-9a-f]{40}$/);
            // The pack declares the bare subtag the validator installs (`zh`), not the
            // browser's regional tag — a `zh-CN` browser is matched to it by primary subtag.
            expect(entry.capabilities?.i18n?.locale).toBe('zh');
            expect(await installedPack(page)).toBeFalsy();

            // --- the offer ---
            const offer = page.locator(OFFER);
            await expect(offer).toBeVisible({ timeout: 15000 });
            // Named by the pack's own registry entry, in its own script — core holds
            // no language names to print.
            await expect(offer).toContainText('简体中文');
            await expect(offer.locator('.locale-offer-dismiss')).toBeVisible();
            expect(await page.evaluate(() => document.documentElement.lang)).toBe('en');

            // --- install, over the real SSE operation ---
            const catalog = page
                .waitForResponse((r) => r.url().includes(CATALOG_PATH) && r.status() === 200, { timeout: 30000 })
                .catch(() => null);
            await offer.locator('.locale-offer-install').click();
            await expect(page.locator('.locale-offer-step-state')).toBeVisible({ timeout: 10000 });

            // The catalog is fetched from the pack's own served directory, and the
            // document language follows the now-enabled pack — with no reload.
            expect(await catalog, `the pack catalog was not served at ${CATALOG_PATH}`).not.toBeNull();
            await expect
                .poll(() => page.evaluate(() => document.documentElement.lang), { timeout: 20000 })
                .toBe('zh');

            const ext = await installedPack(page);
            expect(ext, 'the pack was not installed').toBeTruthy();
            expect(ext.enabled).toBe(true);
            await expect(offer).toHaveCount(0);

            // --- and it is not asked again ---
            await page.reload();
            await page.waitForSelector('.project-item', { timeout: 10000 });
            await expect(offer).toHaveCount(0);
            // The pre-paint cache is what keeps the reload from flashing English.
            expect(await page.evaluate(() => document.documentElement.lang)).toBe('zh');
        });
    });

    test.describe('a zh-CN browser that says no', () => {
        test.use({ locale: 'zh-CN' });

        test('"Don\'t ask again" installs nothing and ends the conversation', async ({ page }) => {
            await page.goto('/');
            await page.waitForSelector('.project-item', { timeout: 10000 });
            const offer = page.locator(OFFER);
            await expect(offer).toBeVisible({ timeout: 15000 });

            await offer.locator('.locale-offer-dismiss').click();
            await expect(offer).toHaveCount(0);
            expect(await installedPack(page)).toBeFalsy();

            // Fresh page, same browser profile → the same IndexedDB record.
            await page.reload();
            await page.waitForSelector('.project-item', { timeout: 10000 });
            await expect(offer).toHaveCount(0);
            expect(await installedPack(page)).toBeFalsy();
        });
    });
});
