import { test, expect, type Browser, type Page } from '@playwright/test';
import { attachVirtualAuthenticator, laneOrigin, mintPairingCode, pairBrowser, signedOutDevice } from '../helpers-auth.mjs';
import { openSettingsTab } from './helpers';

/**
 * Settings → About: the two credential sections of one screen.
 *
 * **Devices & passkeys** — the credentials this install will sign in, the label a person gives each one,
 * the code that adds one more, and the revocation that takes one away. A pairing code authorizes one
 * registration and authenticates nothing, so a row appears only when a ceremony finished; the assertions
 * are about what the server now holds. Most read it back after a reload; the pairing test must not, because
 * the page noticing the device by itself is the subject there.
 *
 * **Sessions** — the logins those ceremonies opened (`GET /api/sessions`, `DELETE /api/sessions/{id}`,
 * `POST /api/sessions/revoke-others`). The About tab marks the caller's own row with a "This device" badge.
 * Revoking another row drops it from the list; revoking the CURRENT row runs the modal's existing sign-out
 * path and lands back on the login screen.
 *
 * Both mutate immediately — they are outside the modal's Save/Cancel flow — and both need a second device,
 * which only a ceremony can make: `pairDevice` opens a signed-out browser, attaches the virtual
 * authenticator and walks the login form. They share one file because they share that ceremony and that
 * tab; a mint is not free (a lane server holds three live pairing codes), so the file runs serially.
 *
 * Runs in the `isolated` project: it registers and revokes the credentials — passkeys and the sessions they
 * opened — that every other spec's session is made of.
 *
 * Session hygiene: the storage-state session the other isolated specs rely on must survive. An "other"
 * session is made by pairing a SECOND device and closing that browser, which revokes nothing server-side;
 * and the "revoke the current session" test signs THIS page into a session of its own first, so the row it
 * revokes is never the shared one.
 *
 * Ceremonies are rate limited per address; the lane server raises that budget for the browser suite
 * (tests/global-setup.ts), and the limit a real install gets is held by the Go tests.
 */
// Confirm button of the app-wide ConfirmationDialog (unique class).
const CONFIRM_BTN = '.btn-confirm';
const TOAST = '.toast';

const SECTION = '.passkeys-section';
const ROW = '.passkeys-section .settings-row';
const NAME = '.passkeys-name';
const NAME_INPUT = '.passkeys-name-input';
const REVOKE = '.passkey-revoke';
const MINT = 'button:has-text("Pair a new device")';
const MINT_NAME = `${SECTION} .settings-create-input`;
const CANCEL_CODES = 'button:has-text("Cancel the code")';
const PAIRING_BOX = '.passkeys-pairing';
const PAIRING_CODE = '.passkeys-code';

const SESSIONS_SECTION = '.sessions-section';
const SESSION_ROW = '.sessions-section .settings-row';
const CURRENT_BADGE = '.session-badge';
const ROW_REVOKE = 'button[aria-label^="Revoke session "]';
const REVOKE_ALL = 'button:has-text("Revoke all other sessions")';
// Login modal that replaces the app when unauthenticated.
const LOGIN_HEADING = '.login-header h2';
const LOGIN_BUTTON = '.login-btn';

/** Open Settings and name the tab this file works in. */
async function openAboutTab(page: Page) {
  await page.goto('/');
  await openSettingsTab(page, 'About');
}

async function openPasskeysSection(page: Page) {
  await openAboutTab(page);
  await expect(page.locator(SECTION)).toBeVisible();
  await expect(page.locator(ROW).first()).toBeVisible({ timeout: 10000 });
}

async function openSessionsSection(page: Page) {
  await openAboutTab(page);
  await expect(page.locator(SESSIONS_SECTION)).toBeVisible();
  await expect(page.locator(SESSION_ROW).first()).toBeVisible({ timeout: 10000 });
}

/** The caller's own row: the one carrying the "This device" badge. */
function currentRow(page: Page) {
  return page.locator(`${SESSION_ROW}:has(${CURRENT_BADGE})`);
}

/** Locator for the session row whose data-session-id matches. */
function rowSessionId(page: Page, id: string) {
  return page.locator(`${SESSION_ROW}[data-session-id="${id}"]`);
}

/**
 * Pair one more device with this install, through the login form a person uses.
 *
 * The new browser is signed out when it opens — `signedOutDevice` empties what `browser.newContext()`
 * would otherwise inherit from the project — spends `code`, and is closed once it is in. Closing it
 * revokes nothing: the passkey stays on the list, and so does the session it opened.
 */
async function pairDevice(browser: Browser, code: string, opts: { name?: string; link?: string } = {}) {
  const { context, page: device } = await signedOutDevice(browser);
  try {
    await pairBrowser(device, laneOrigin(), code, opts);
  } finally {
    await context.close();
  }
}

/** Pair a device with a code minted through the API: the "other" session the session list is about. */
async function addOtherSession(page: Page, browser: Browser): Promise<void> {
  const pairing = await mintPairingCode(page, laneOrigin());
  await pairDevice(browser, pairing.code);
}

/**
 * Sign THIS page into a session of its own, so the row it goes on to revoke is not the shared one.
 *
 * The pairing code is minted while the page still holds the shared session, the cookies are then dropped,
 * and the page walks the login gate with its own fresh passkey. What the context keeps afterwards is a
 * second session, and the shared one is left alone for the next spec.
 */
async function signInWithFreshPasskey(page: Page): Promise<void> {
  const pairing = await mintPairingCode(page, laneOrigin());
  await attachVirtualAuthenticator(page.context(), page);
  await page.context().clearCookies();
  await pairBrowser(page, laneOrigin(), pairing.code);
}

/** Re-read the session list straight from the API (browser's own session). */
async function apiSessions(page: Page): Promise<Array<{ id: string; current: boolean }>> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/sessions', { credentials: 'include' });
    return res.json();
  });
}

test.describe.configure({ mode: 'serial' });
test.describe('Settings → About: devices, passkeys and sessions', () => {

  test('lists the passkeys this install holds', async ({ page }) => {
    await openPasskeysSection(page);
    const rows = page.locator(ROW);
    expect(await rows.count()).toBeGreaterThan(0);
    // A row says what the device is and when it joined; it never shows a credential.
    await expect(rows.first().locator(NAME)).not.toHaveText('');
    await expect(rows.first().getByText('Added')).toBeVisible();
    expect(await page.locator(`${SECTION} code`).count()).toBe(0);
  });

  test('renames a device, and the label survives a reload', async ({ page }) => {
    await openPasskeysSection(page);
    const name = `Probe laptop ${Date.now()}`;
    await page.locator(ROW).first().locator(NAME).click();
    await expect(page.locator(NAME_INPUT)).toBeVisible();
    await page.fill(NAME_INPUT, name);
    await page.keyboard.press('Enter');
    await expect(page.locator(ROW).first().locator(NAME)).toHaveText(name);

    await page.reload();
    await openPasskeysSection(page);
    await expect(page.locator(`${ROW}:has-text(${JSON.stringify(name)})`)).toBeVisible({ timeout: 10000 });
  });

  test('pairing a new device shows a code, a countdown and a QR, and the code can be cancelled', async ({ page }) => {
    await openPasskeysSection(page);
    await page.locator(MINT).click();
    await expect(page.locator(PAIRING_BOX)).toBeVisible();

    // The code is two groups of four, the shape a person reads aloud.
    await expect(page.locator('.passkeys-code')).toHaveText(/^[0-9A-Z]{4}-[0-9A-Z]{4}$/);
    // The picture is the same link, for a device that can scan rather than type.
    await expect(page.locator('img.passkeys-qr')).toHaveAttribute('src', /^data:image\/png;base64,/);
    await expect(page.locator('.passkeys-pairing-countdown')).toContainText('expires in');
    await expect(page.locator('.passkeys-url')).not.toBeEmpty();

    await page.locator(CANCEL_CODES).click();
    await page.locator(CONFIRM_BTN).click();
    await expect(page.locator(PAIRING_BOX)).toHaveCount(0);
  });

  test('a device paired with a minted code appears by itself, and can be revoked again', async ({ page, browser }) => {
    await openPasskeysSection(page);
    const before = await page.locator(ROW).count();

    // The mint button is part of the subject: the watch that notices the new device starts from this click.
    await page.locator(MINT).click();
    const code = ((await page.locator(PAIRING_CODE).textContent()) ?? '').trim();
    expect(code).toMatch(/^[0-9A-Z]{4}-[0-9A-Z]{4}$/);

    await pairDevice(browser, code);

    // No reload anywhere below. The section re-reads the list while the code is live, that row is the code's
    // spend, and the toast names the device the server labelled from its own browser.
    await expect(page.locator(TOAST)).toContainText('is paired', { timeout: 15000 });
    // Oldest first, so the device that just joined is the last row.
    await expect(page.locator(ROW)).toHaveCount(before + 1, { timeout: 10000 });
    await expect(page.locator(PAIRING_BOX)).toHaveCount(0);

    await page.locator(ROW).last().locator(REVOKE).click();
    await page.locator(CONFIRM_BTN).click();
    await expect(page.locator(ROW)).toHaveCount(before, { timeout: 10000 });
  });

  test('a name typed before minting rides the link and names the device that pairs with it', async ({ page, browser }) => {
    const name = `Scanned tablet ${Date.now()}`;
    await openPasskeysSection(page);
    await page.fill(MINT_NAME, name);
    await page.locator(MINT).click();
    await expect(page.locator(PAIRING_BOX)).toBeVisible();

    // The name travels in the link, not in the code; `+` is the fragment's spelling of a space.
    const link = (await page.locator('.passkeys-url').textContent()) ?? '';
    expect(link).toContain(`&name=${name.replace(/ /g, '+')}`);

    const code = (await page.locator(PAIRING_CODE).textContent()) ?? '';
    // The link is the only thing that carries the name.
    await pairDevice(browser, code, { link: link.trim() });

    await page.reload();
    await openPasskeysSection(page);
    await expect(page.locator(`${ROW}:has-text(${JSON.stringify(name)})`)).toBeVisible({ timeout: 10000 });
  });

  test('About lists sessions and marks this device', async ({ page }) => {
    await openSessionsSection(page);

    // Exactly one "This device" badge, on a visible row.
    await expect(currentRow(page).first()).toBeVisible();
    await expect(currentRow(page)).toHaveCount(1);
    await expect(currentRow(page).locator(CURRENT_BADGE)).toHaveText('This device');

    // The current row carries its metadata + a stable, test-friendly revoke label.
    const row = currentRow(page).first();
    await expect(row.locator('.session-meta-item', { hasText: 'Created' })).toBeVisible();
    await expect(row.locator('.session-meta-item', { hasText: 'Last seen' })).toBeVisible();
    await expect(row.locator('.session-meta-item', { hasText: 'IP' })).toBeVisible();
    const ua = row.locator('.settings-row-label');
    await expect(ua).toBeVisible();
    await expect(ua).toHaveAttribute('title', /.+/);

    // Revoke affordance: a translated name that carries the row's own id.
    const label = await row.locator(ROW_REVOKE).getAttribute('aria-label');
    expect(label).toMatch(/^Revoke session .+$/);
    // The row's own id is in the name (and in data-session-id).
    const sessionId = await apiSessions(page).then(s => s.find(x => x.current)?.id ?? '');
    expect(label).toBe(`Revoke session ${sessionId}`);
    await expect(row).toHaveAttribute('data-session-id', sessionId);
  });

  test('revoke all other sessions leaves this device', async ({ page, browser }) => {
    await openSessionsSection(page);
    const sessionId = await apiSessions(page).then(s => s.find(x => x.current)?.id ?? '');

    // Seed one "other" session (its browser is closed — the page keeps its own session).
    await addOtherSession(page, browser);
    await page.locator('button[aria-label="Reload the session list"]').click();
    await expect(async () => {
      expect(await page.locator(SESSION_ROW).count()).toBeGreaterThanOrEqual(2);
      expect((await apiSessions(page)).length).toBeGreaterThanOrEqual(2);
    }).toPass({ timeout: 10000 });

    await page.locator(REVOKE_ALL).click();
    await page.locator(CONFIRM_BTN).click();

    // Only this device remains, both in the UI and on the server.
    await expect(page.locator(SESSION_ROW)).toHaveCount(1, { timeout: 10000 });
    await expect(currentRow(page)).toHaveCount(1);
    await expect(currentRow(page).locator(CURRENT_BADGE)).toHaveText('This device');
    await expect(rowSessionId(page, sessionId)).toHaveCount(1);
    await expect(page.locator(REVOKE_ALL)).toBeDisabled();

    const remaining = await apiSessions(page);
    expect(remaining).toHaveLength(1);
    expect(remaining[0].current).toBe(true);
    expect(remaining[0].id).toBe(sessionId);

    // With no other rows left, the empty state is shown and the bulk action idles.
    await expect(page.locator(`${SESSIONS_SECTION} .settings-list-status`)).toHaveText('No other active sessions');
  });

  test('revoking the current session returns to the login screen', async ({ page }) => {
    // Sign this page into a session of its own first: the row it is about to revoke is
    // the row this page holds, and the shared storage-state session other isolated
    // specs sign in with has to survive it.
    await signInWithFreshPasskey(page);

    await openSessionsSection(page);
    await expect(currentRow(page)).toHaveCount(1);
    await page.locator(SESSION_ROW).first().waitFor();

    await currentRow(page).locator(ROW_REVOKE).click();
    await page.locator(CONFIRM_BTN).click();

    // The app is back at the login modal, and it offers a passkey — this install has one.
    await expect(page.locator(LOGIN_BUTTON)).toBeVisible({ timeout: 10000 });
    await expect(page.locator(LOGIN_HEADING)).toHaveText('Sign in');
    await expect(page.locator('.sidebar')).toHaveCount(0);
  });
});
