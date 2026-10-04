/**
 * Signing a real browser in with a real passkey.
 *
 * MIKE has no test password and no test-only credential: a session is what a finished WebAuthn ceremony
 * earns, so anything that needs one drives a ceremony. Chromium lets a test do that without a hardware
 * key — the DevTools protocol attaches a **virtual authenticator** to the page — and the pairing code
 * comes out of the server's own log, from the same boxed banner an operator reads on a fresh install.
 *
 * This is the honest path, and it is also the slow one: a browser, a ceremony and the login form are all
 * in the way, which is exactly what a harness that skipped them would stop testing.
 *
 * Plain ESM so both the Playwright lane (`global-setup.ts`) and the ad-hoc `*-check.mjs` probes can
 * import the same rules.
 */
import { chromium, test } from '@playwright/test';

/**
 * The lane server this test project talks to, as an origin.
 *
 * The helper functions take an absolute base because they mix page navigation with raw API calls, and
 * `page.url()` is `about:blank` until the test has navigated. The project's `baseURL` is the address the
 * lane was started on, and it differs between the chromium and isolated projects.
 */
export function laneOrigin() {
    const base = test.info().project.use.baseURL;
    if (!base) throw new Error('this project sets no baseURL, so the lane server has no address to pair with');
    return new URL(base).origin;
}

/**
 * A context that holds nothing — the browser that has never paired.
 *
 * `browser.newContext()` alone is not that: Playwright applies the project's `use` options to every
 * context a test creates from the `browser` fixture, so a bare `newContext()` starts **signed in** with
 * the lane's storage state. Emptying it is the only way a spec reaches the login gate in a second
 * browser.
 */
export function signedOutContext(browser) {
    return browser.newContext({ storageState: { cookies: [], origins: [] } });
}

/**
 * The row `auth.PairingBanner` prints; backend/auth/pairing_banner.go owns the shape. The banner carries no
 * record prefix, so nothing may follow the code but the box's own padding and right rule.
 */
const PAIRING_CODE_ROW = /PAIRING CODE\s+([0-9A-Z]{4}-[0-9A-Z]{4})[ │]*$/;

/** The code in a server log's most recent boot banner, or '' when the log holds none. */
export function pairingCodeFromLog(text) {
    let code = '';
    for (const line of String(text ?? '').split('\n')) {
        const match = PAIRING_CODE_ROW.exec(line);
        if (match) code = match[1];
    }
    return code;
}

/** Wait for a booted server to print its pairing code. A paired install prints none, and then this is
 *  the failure to read: the data directory was reused, so nothing needs pairing. */
export async function readPairingCode(logPath, { timeoutMs = 30000 } = {}) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
        let text = '';
        try {
            text = (await import('node:fs')).readFileSync(logPath, 'utf8');
        } catch {
            text = '';
        }
        const code = pairingCodeFromLog(text);
        if (code) return code;
        if (Date.now() > deadline) {
            throw new Error(`no pairing code was printed to ${logPath} within ${timeoutMs} ms — `
                + 'a data directory that already holds a passkey prints none (delete it, or run --reset-passkeys)');
        }
        await new Promise((res) => setTimeout(res, 200));
    }
}

/**
 * A Chromium whose page can answer a passkey prompt without a device.
 *
 * MIKE's ceremony asks for a resident key and for user verification, so the virtual authenticator is
 * built with both: `hasResidentKey` is what makes the credential discoverable (the assertion sends no
 * credential id, so the key has to live on the authenticator), and `isUserVerified` is the bit the
 * server's `userVerification: required` reads. `automaticPresenceSimulation`, the default, is what keeps
 * an untouched prompt from hanging the test.
 */
export async function attachVirtualAuthenticator(context, page) {
    const cdp = await context.newCDPSession(page);
    await cdp.send('WebAuthn.enable');
    const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
        options: {
            protocol: 'ctap2',
            transport: 'internal',
            hasResidentKey: true,
            hasUserVerification: true,
            isUserVerified: true,
        },
    });
    return { cdp, authenticatorId };
}

export async function openPairingBrowser({ headless = true } = {}) {
    const browser = await chromium.launch({ headless, chromiumSandbox: false });
    // The lane server signs its leaf with a CA created in its own data directory, which no browser on
    // this machine trusts. The ceremony itself is what is under test, so the certificate is accepted;
    // the pairing dialog's CA download is the artifact that makes it trustworthy for real.
    const context = await browser.newContext({ ignoreHTTPSErrors: true });
    const page = await context.newPage();
    const { cdp, authenticatorId } = await attachVirtualAuthenticator(context, page);
    return { browser, context, page, cdp, authenticatorId };
}

/** The login form's own submit, whatever body it is showing: passkey or pairing code. */
async function clickPrimary(page) {
    await page.waitForSelector('.login-modal', { timeout: 30000 });
    await page.waitForSelector('.login-btn:not([disabled])', { timeout: 30000 });
    await page.click('.login-btn');
}

/** Wait for the gate to open: the login form is gone and the app shell is on screen. */
async function waitForApp(page) {
    await page.waitForSelector('.app', { state: 'visible', timeout: 30000 });
}

/**
 * Pair a browser with a code, through the login form a person uses.
 *
 * The code rides in the URL fragment because that is how the server writes its pairing link, and the
 * form is expected to take it from there and strip it out of the address bar again.
 */
export async function pairBrowser(page, base, code) {
    await page.goto(`${base}/#pair=${code}`, { waitUntil: 'commit' });
    await page.waitForSelector('#login-code', { state: 'visible', timeout: 30000 });
    const typed = await page.inputValue('#login-code');
    // Compare in one shape: the log's code carries the dash the form also accepts.
    const shape = (value) => String(value).replace(/[^0-9A-Za-z]/g, '').toUpperCase();
    if (shape(typed) !== shape(code)) {
        throw new Error(`the pairing link did not fill the code field (it holds ${JSON.stringify(typed)})`);
    }
    if (page.url().includes('#pair=')) {
        throw new Error('the pairing link left the code in the address bar; it belongs to the form alone');
    }
    await clickPrimary(page);
    await waitForApp(page);
}

/** Sign a paired browser in with the passkey it holds — the assertion, on the same page. */
export async function signInBrowser(page, base) {
    await page.goto(base + '/', { waitUntil: 'commit' });
    await page.waitForSelector('.login-btn:not([disabled])', { timeout: 30000 });
    await page.click('.login-btn');
    await waitForApp(page);
}

/** This browser's own session, in the two forms the harness needs: a Cookie header for the raw HTTP
 *  helpers, and the CSRF token for the writes. */
export async function sessionOf({ context, page }, base) {
    const answer = await (await page.request.get(`${base}/api/auth/session`)).json();
    if (!answer?.authenticated) throw new Error(`the ceremony did not open a session at ${base}`);
    const cookie = (await context.cookies(base)).find((c) => c.name === 'PHPSESSID');
    if (!cookie) throw new Error(`no session cookie was set at ${base}`);
    return { cookie: `PHPSESSID=${cookie.value}`, csrfToken: answer.csrf_token || '' };
}

/**
 * Everything one server needs to hand out to its specs: a browser that pairs itself once, and the
 * session that walk produced. The browser is closed here — the specs replay the cookie from Playwright's
 * storage state, so holding a second browser open for the run would buy nothing.
 */
export async function pairAndReadSession(base, code, { close = true } = {}) {
    const opened = await openPairingBrowser();
    try {
        await pairBrowser(opened.page, base, code);
        return await sessionOf(opened, base);
    } finally {
        if (close) await opened.browser.close();
    }
}

/** The code an ad-hoc probe pairs with, or the instruction for getting one. */
export function pairingCodeFromEnv() {
    const code = (process.env.MIKE_PAIR_CODE || '').trim();
    if (!code) {
        throw new Error('MIKE_PAIR_CODE is not set. Mint one in Settings → Devices & passkeys on a browser '
            + 'that is already signed in, or read it off an unpaired server\'s boot banner — the boxed row that '
            + 'says "PAIRING CODE  XXXX-XXXX". A code works once, so mint one per probe run.');
    }
    return code;
}

/**
 * A code that lets one more device pair, minted through the API by a browser that already has a session.
 *
 * This is the Settings action, called the way a script would: pairing is something a signed-in device
 * decides, so a test that needs a second device asks for a code rather than reading one out of a log.
 *
 * A lane server holds at most three live codes (`pairingMaxLive` in `backend/auth/pairing.go`), and the
 * workers mint concurrently, so a full cap is the lane being busy rather than a fault. A spent code frees
 * its slot as soon as the ceremony that spent it finishes, which is about a second, so the wait is bounded
 * and short; the cap itself is a product rule and stays unraised.
 */
export async function mintPairingCode(page, base, { timeoutMs = 20000 } = {}) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
        const session = await (await page.request.get(`${base}/api/auth/session`)).json();
        if (!session?.authenticated) throw new Error(`no session at ${base}, so no pairing code can be minted`);
        const res = await page.request.post(`${base}/api/auth/pairing`, {
            headers: { 'X-CSRF-Token': session.csrf_token || '' },
        });
        const answer = await res.json();
        if (answer?.code) return answer;
        if (res.status() !== 409 || Date.now() > deadline) {
            throw new Error(`minting a pairing code answered ${res.status()} ${JSON.stringify(answer)}`);
        }
        await new Promise((resolve) => setTimeout(resolve, 250));
    }
}
