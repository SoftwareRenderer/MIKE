import { test, expect, type Page } from '@playwright/test';

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
//   * extra ("other") sessions are created with `credentials: 'omit'`, so the
//     browser context never adopts the resulting cookie;
//   * the "revoke the current session" test first adopts a THROWAWAY session
//     (login through the browser context, which does store the cookie), so the
//     revoked row is that throwaway session and not the shared storage-state one.
//
// Login rate limiting on the test server is 5 logins/IP/minute — this spec does
// exactly two, in two different tests.

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
const LOGIN_PASSWORD_INPUT = '#login-password';

// Matches TEST_PASSWORD in tests/global-setup.ts (same value login.spec.ts uses).
const TEST_PASSWORD = 'test-password-1234';

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
 * Create an additional login session server-side WITHOUT the browser adopting it
 * (credentials: 'omit' → the response Set-Cookie is discarded), so it shows up as
 * an "other" row for the caller's own session.
 */
async function addOtherSession(page: Page): Promise<void> {
  const res = await page.evaluate(async (password) => {
    const r = await fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'omit',
      body: JSON.stringify({ password }),
    });
    return { status: r.status };
  }, TEST_PASSWORD);
  expect(res.status, 'extra session should be created by logging in').toBe(200);
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
    const ua = row.locator('.session-agent');
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

  test('revoke all other sessions leaves this device', async ({ page }) => {
    await openSessionsSection(page);
    const sessionId = await apiSessions(page).then(s => s.find(x => x.current)?.id ?? '');

    // Seed one "other" session (cookie discarded — the page keeps its own session).
    await addOtherSession(page);
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
    await expect(page.locator(`${SESSIONS_SECTION} .sessions-status`)).toHaveText('No other active sessions');
  });

  test('revoking the current session returns to the login screen', async ({ page }) => {
    // Adopt a throwaway session first: revoking it must not delete the shared
    // storage-state session other isolated specs sign in with.
    const login = await page.context().request.post('/api/auth/login', {
      data: { password: TEST_PASSWORD },
    });
    expect(login.status()).toBe(200);

    await openSessionsSection(page);
    await expect(currentRow(page)).toHaveCount(1);
    await page.locator(SESSION_ROW).first().waitFor();

    await currentRow(page).locator(ROW_REVOKE).click();
    await page.locator(CONFIRM_BTN).click();

    // The app is back at the login modal.
    await expect(page.locator(LOGIN_PASSWORD_INPUT)).toBeVisible({ timeout: 10000 });
    await expect(page.locator(LOGIN_HEADING)).toHaveText('Sign in');
    await expect(page.locator('.sidebar')).toHaveCount(0);
  });
});

/** Locator for the row whose data-session-id matches. */
function rowSessionId(page: Page, id: string) {
  return page.locator(`${SESSION_ROW}[data-session-id="${id}"]`);
}
