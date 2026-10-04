import { test, expect, type Browser, type Page } from '@playwright/test';
import { attachVirtualAuthenticator, laneOrigin, mintPairingCode, pairBrowser, signedOutContext } from '../helpers-auth.mjs';

// Sessions manager in Settings → About (GET /api/sessions, DELETE /api/sessions/{id},
// POST /api/sessions/revoke-others).
//
// The About tab lists every live login session of the signed-in user and marks the
// caller's own row with a "This device" badge. Revoking another row drops it from
// the list; revoking the CURRENT row runs the modal's existing sign-out path and
// lands back on the login screen.
//
// Runs in the `isolated` project (settings UI + it mutates server session state).
//
// Session hygiene — the storage-state session other isolated specs rely on must
// survive this spec:
//   * an extra ("other") session is made by pairing a SECOND device — its own browser,
//     its own passkey, opened with a pairing code this page mints — and closing that
//     browser leaves the session alive server-side, which is exactly the row under test;
//   * the "revoke the current session" test pairs that second device first and revokes
//     ITS row, so the revoked row is never the shared storage-state one.
//
// Sign-in is a passkey ceremony, so a device cannot be conjured with a request: the
// helper attaches Chromium's virtual authenticator and walks the login form.
//
// Ceremonies are rate limited per address; the lane server raises that budget for the browser
// suite (tests/global-setup.ts), and the limit a real install gets is held by the Go tests.

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const ABOUT_TAB = 'button.tab-btn:has-text("About")';
const SESSIONS_SECTION = '.sessions-section';
const SESSION_ROW = '.sessions-section .settings-row';
const CURRENT_BADGE = '.session-badge';
const ROW_REVOKE = 'button[aria-label^="Revoke session "]';
const REVOKE_ALL = 'button:has-text("Revoke all other sessions")';
// Confirm button of the app-wide ConfirmationDialog (unique class).
const CONFIRM_BTN = '.btn-confirm';
// Login modal that replaces the app when unauthenticated.
const LOGIN_HEADING = '.login-header h2';
const LOGIN_BUTTON = '.login-btn';

/** Open Settings, switch to the About tab and wait for the session list to render. */
async function openSessionsSection(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.add-task-btn, .icon-btn[aria-label="Settings"]', { timeout: 10000 });
  await page.locator(SETTINGS_BTN).first().click();
  await page.locator(ABOUT_TAB).click();
  await expect(page.locator(SESSIONS_SECTION)).toBeVisible();
  await expect(page.locator(SESSION_ROW).first()).toBeVisible({ timeout: 10000 });
}

/** The caller's own row: the one carrying the "This device" badge. */
function currentRow(page: Page) {
  return page.locator(`${SESSION_ROW}:has(${CURRENT_BADGE})`);
}

/**
 * Pair a second device with this install and leave its session behind.
 *
 * The new browser is signed out when it opens, spends a pairing code this page mints, and is closed once
 * it is in. Closing it revokes nothing: the session it opened is the "other" row this spec is about.
 */
async function addOtherSession(page: Page, browser: Browser): Promise<void> {
  const base = laneOrigin();
  const pairing = await mintPairingCode(page, base);
  const context = await signedOutContext(browser);
  try {
    const device = await context.newPage();
    await attachVirtualAuthenticator(context, device);
    await pairBrowser(device, base, pairing.code);
  } finally {
    await context.close();
  }
}

/**
 * Sign THIS page into a session of its own, so the row it goes on to revoke is not the shared one.
 *
 * The pairing code is minted while the page still holds the shared session, the cookies are then dropped,
 * and the page walks the login gate with its own fresh passkey. What the context keeps afterwards is a
 * second session, and the shared one is left alone for the next spec.
 */
async function signInWithFreshPasskey(page: Page): Promise<void> {
  const base = laneOrigin();
  const pairing = await mintPairingCode(page, base);
  await attachVirtualAuthenticator(page.context(), page);
  await page.context().clearCookies();
  await pairBrowser(page, base, pairing.code);
}

/** Re-read the session list straight from the API (browser's own session). */
async function apiSessions(page: Page): Promise<Array<{ id: string; current: boolean }>> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/sessions', { credentials: 'include' });
    return res.json();
  });
}

test.describe.serial('Settings → About sessions', () => {
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

    // Seed one "other" session (cookie discarded — the page keeps its own session).
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

/** Locator for the row whose data-session-id matches. */
function rowSessionId(page: Page, id: string) {
  return page.locator(`${SESSION_ROW}[data-session-id="${id}"]`);
}
