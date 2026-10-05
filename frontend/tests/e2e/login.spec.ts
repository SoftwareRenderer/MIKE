import { test, expect, type Browser, type Page } from '@playwright/test';
import {
    attachVirtualAuthenticator,
    laneOrigin,
    mintPairingCode,
    pairBrowser,
    signInBrowser,
    signedOutContext,
} from '../helpers-auth.mjs';

/**
 * Getting in: the passkey assertion, and the pairing code that buys the first one.
 *
 * `[role="dialog"]` is not a login-modal selector here — BaseModal, CommitDiffOverlay, TerminalPanel and
 * every ChatMessage edit `<dialog>` carry it — so the gate is named by its own class, `.login-modal`.
 *
 * A ceremony needs a browser, so these tests drive Chromium with a virtual authenticator
 * (`helpers-auth.mjs`) and mint their codes through the API the way Settings does. Nothing here is a
 * test-only credential: a code is spent on a registration, and the session belongs to whichever browser
 * finished that registration. A ceremony also cannot be repeated with the same code, so every test that
 * pairs mints its own.
 */
const loginDialog = (page: Page) => page.locator('.login-modal');

/**
 * The lane's own origin — the helper talks to the API with absolute URLs, and `page.url()` is
 * `about:blank` before the test's first navigation.
 */
const baseOf = () => laneOrigin();

/** A signed-out browser that can answer a passkey prompt — no cookies, its own authenticator. */
async function signedOutBrowser(browser: Browser) {
    const context = await signedOutContext(browser);
    const page = await context.newPage();
    await attachVirtualAuthenticator(context, page);
    return { context, page };
}

// One test at a time: a lane server holds three live pairing codes, and this file is the only thing on
// that server that mints them, so running the file serially is what keeps a mint from meeting a full cap.
// (`mintPairingCode` still waits out the cap, because another worker's ceremony can be the one holding it.)
test.describe.configure({ mode: 'serial' });
test.describe('Passkey login', () => {
    /** Sign this page out and show its gate. Minting a code needs the session this fixture starts with,
     *  so signing out is a step a test takes rather than something the fixture does to all of them. */
    const signOut = async (page: Page) => {
        await page.context().clearCookies();
        await page.goto('/');
    };

    test('a signed-out browser is asked for a passkey', async ({ page }) => {
        await signOut(page);
        await expect(loginDialog(page)).toBeVisible({ timeout: 10000 });
        await expect(loginDialog(page).locator('h2')).toHaveText('Sign in');
        await expect(loginDialog(page).locator('.login-btn')).toHaveText('Sign in with a passkey');
        await expect(loginDialog(page).getByText('I have a pairing code')).toBeVisible();
        await expect(page.locator('.sidebar')).not.toBeVisible();
    });

    test('the login modal is not dismissed by a click outside it', async ({ page }) => {
        await signOut(page);
        await expect(loginDialog(page)).toBeVisible({ timeout: 10000 });
        await page.locator('.overlay').click();
        await expect(loginDialog(page)).toBeVisible();
        await expect(page.locator('.sidebar')).not.toBeVisible();
    });

    test('a pairing link fills the code field and leaves the address bar', async ({ page }) => {
        // The code rides in the URL fragment because that is how the server writes its pairing link —
        // a query string would reach a proxy's access log, and a code is a credential until it is spent.
        const pairing = await mintPairingCode(page, baseOf());
        await page.context().clearCookies();
        await page.goto(`/#pair=${pairing.code}`);

        const field = page.locator('#login-code');
        await expect(field).toBeVisible({ timeout: 10000 });
        await expect(field).toHaveValue(pairing.code);
        expect(page.url()).not.toContain('#pair=');
        await expect(loginDialog(page).locator('h2')).toHaveText('Pair this device');
    });

    test('a pairing code creates a passkey and opens the session on that browser', async ({ page, browser }) => {
        const base = baseOf();
        const pairing = await mintPairingCode(page, base);

        const device = await signedOutBrowser(browser);
        try {
            await pairBrowser(device.page, base, pairing.code);
            await expect(device.page.locator('.project-item').first()).toBeVisible({ timeout: 10000 });

            // The registration left a device on the list, named by what the browser said it was.
            const rows = await (await device.page.request.get(`${base}/api/auth/passkeys`)).json();
            expect(Array.isArray(rows) && rows.length).toBeTruthy();
            expect(rows.some((row: { name: string }) => row.name?.length > 0)).toBe(true);
        } finally {
            await device.context.close();
        }
    });

    test('a device named while it pairs is listed under the name it was given', async ({ page, browser }) => {
        const base = baseOf();
        // Unique because every spec on this lane server adds rows to the same list.
        const name = `Probe tablet ${Date.now()}`;
        const pairing = await mintPairingCode(page, base);

        const device = await signedOutBrowser(browser);
        try {
            await pairBrowser(device.page, base, pairing.code, { name });

            // The row carries what was typed here, not the label the browser would have been given.
            const rows = await (await device.page.request.get(`${base}/api/auth/passkeys`)).json();
            expect(rows.filter((row: { name: string }) => row.name === name)).toHaveLength(1);
        } finally {
            await device.context.close();
        }
    });

    test('the passkey signs the same browser back in', async ({ page, browser }) => {
        const base = baseOf();
        const pairing = await mintPairingCode(page, base);

        const device = await signedOutBrowser(browser);
        try {
            await pairBrowser(device.page, base, pairing.code);
            const csrf = (await (await device.page.request.get(`${base}/api/auth/session`)).json()).csrf_token;
            expect(await (await device.page.request.post(`${base}/api/auth/logout`, {
                headers: { 'X-CSRF-Token': csrf },
            })).status()).toBe(200);

            // Back at the gate, and this time there is a credential to ask for: the code field is gone.
            await device.page.goto(`${base}/`, { waitUntil: 'commit' });
            await expect(loginDialog(device.page)).toBeVisible({ timeout: 10000 });
            await expect(device.page.locator('#login-code')).toHaveCount(0);

            await signInBrowser(device.page, base);
            await expect(device.page.locator('.project-item').first()).toBeVisible({ timeout: 10000 });
        } finally {
            await device.context.close();
        }
    });

    test('a browser whose authenticator holds nothing stays signed out', async ({ page, browser }) => {
        // A ctap2 authenticator with no resident credential has nothing to answer with. That is a
        // dismissal, not a fault, and it must not open a session either way.
        const device = await signedOutBrowser(browser);
        try {
            await device.page.goto(`${baseOf()}/`, { waitUntil: 'commit' });
            await device.page.click('.login-btn');
            await expect(device.page.locator('.login-message')).toBeVisible({ timeout: 20000 });
            await expect(loginDialog(device.page)).toBeVisible();
            await expect(device.page.locator('.sidebar')).toHaveCount(0);
        } finally {
            await device.context.close();
        }
    });

    test('a spent pairing code is refused, and the code field stays put', async ({ page, browser }) => {
        const base = baseOf();
        const pairing = await mintPairingCode(page, base);

        const first = await signedOutBrowser(browser);
        try {
            await pairBrowser(first.page, base, pairing.code);
        } finally {
            await first.context.close();
        }

        const second = await signedOutBrowser(browser);
        try {
            await second.page.goto(`${base}/#pair=${pairing.code}`, { waitUntil: 'commit' });
            await second.page.waitForSelector('#login-code', { timeout: 20000 });
            await second.page.click('.login-btn');
            await expect(second.page.locator('.login-message')).toHaveText('pairing code is not valid', { timeout: 20000 });
            await expect(loginDialog(second.page)).toBeVisible();
            await expect(second.page.locator('#login-code')).toBeVisible();
        } finally {
            await second.context.close();
        }
    });

    test('a code typed by hand pairs a device too', async ({ page, browser }) => {
        // The path a phone takes when nobody can scan a QR: type the eight characters.
        const base = baseOf();
        const pairing = await mintPairingCode(page, base);

        const device = await signedOutBrowser(browser);
        try {
            await device.page.goto(base + '/', { waitUntil: 'commit' });
            await device.page.getByText('I have a pairing code').click();
            await device.page.fill('#login-code', pairing.code.toLowerCase());
            await expect(device.page.locator('.login-btn')).toBeEnabled();
            await device.page.click('.login-btn');
            await expect(device.page.locator('.project-item').first()).toBeVisible({ timeout: 20000 });
        } finally {
            await device.context.close();
        }
    });
});
