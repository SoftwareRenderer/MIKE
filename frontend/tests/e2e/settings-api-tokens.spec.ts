import { test, expect, type Page } from '@playwright/test';
import { openSettingsTab } from './helpers';

// API tokens manager in Settings → About (GET/POST /api/tokens, DELETE /api/tokens/{id}).
//
// The section issues a token whose plaintext is shown exactly once, lists the rows by label, and
// revokes on click. What the issued credential may and may not do — the scope, the refusal outside it,
// the stamping of a use and the immediacy of a revocation — is the server's answer, and it is proven
// through the real middleware chain in `backend/server/token_auth_test.go` and
// `backend/store/api_tokens_test.go`; a second copy from the browser would assert the same table against
// the same route, so nothing here carries an `Authorization` header.
//
// Runs in the `isolated` project (settings UI + it changes server token state), and revokes every
// token it creates so the lane server is left with none.

const TOKENS_SECTION = '.tokens-section';
const TOKEN_ROW = '.tokens-section .settings-row';
const TOKEN_LABEL_INPUT = '.tokens-section input[type="text"]';
const CREATE_BTN = '.tokens-section button.btn-primary';
const SECRET = '.tokens-secret';
const CONFIRM_BTN = '.btn-confirm';

/** Open Settings, switch to the About tab and scroll the API tokens section into view. */
async function openTokensSection(page: Page) {
  await page.goto('/');
  await openSettingsTab(page, 'About');
  await expect(page.locator(TOKENS_SECTION)).toBeVisible();
  await page.locator(TOKENS_SECTION).scrollIntoViewIfNeeded();
}

/** Create a token through the UI and return the secret it revealed. */
async function createToken(page: Page, label: string): Promise<string> {
  await page.locator(TOKEN_LABEL_INPUT).fill(label);
  await page.locator(CREATE_BTN).click();
  await expect(page.locator(SECRET)).toBeVisible();
  const secret = (await page.locator(SECRET).textContent()) ?? '';
  await expect(page.locator(`${TOKEN_ROW}:has-text("${label}")`)).toBeVisible();
  return secret.trim();
}

/** Revoke through the UI and wait for the row to go. */
async function revokeToken(page: Page, id: string) {
  await page.locator(TOKENS_SECTION).scrollIntoViewIfNeeded();
  await page.locator(`button[aria-label="Revoke token ${id}"]`).click();
  await page.locator(CONFIRM_BTN).click();
  await expect(page.locator(`${TOKEN_ROW}[data-token-id="${id}"]`)).toHaveCount(0, { timeout: 10000 });
}

/** The server's own list — the row ids are what the UI's revoke labels must carry. */
async function apiTokens(page: Page): Promise<Array<{ id: string; label: string }>> {
  return await page.evaluate(async () => {
    const res = await fetch('/api/tokens', { credentials: 'include' });
    return res.json();
  });
}

test.describe.serial('Settings → About API tokens', () => {
  test('issues a token, shows it once, and lists it by label only', async ({ page }) => {
    await openTokensSection(page);
    const secret = await createToken(page, 'playwright importer');
    expect(secret.startsWith('mike_')).toBe(true);

    // A fresh read of the list — the listing identifies the row by its label and nothing else.
    const rows = await apiTokens(page);
    const row = rows.find(r => r.label === 'playwright importer');
    expect(row, 'the created token is listed').toBeTruthy();
    expect(JSON.stringify(rows)).not.toContain(secret);

    // Closing the modal discards the secret: it lives in that component and nowhere else, so
    // reopening About shows the row by label with no way to see the value again. The Settings modal
    // renders two close buttons (its own header and BaseModal's), hence first().
    await page.locator('button.close-btn').first().click();
    await openSettingsTab(page, 'About');
    await page.locator(TOKENS_SECTION).scrollIntoViewIfNeeded();
    await expect(page.locator(TOKEN_ROW, { hasText: 'playwright importer' })).toBeVisible();
    await expect(page.locator(SECRET)).toHaveCount(0);
    await expect(page.locator(TOKENS_SECTION)).not.toContainText(secret);

    await revokeToken(page, row!.id);
    expect((await apiTokens(page)).find(r => r.id === row!.id)).toBeUndefined();
  });

  test('a label is required', async ({ page }) => {
    await openTokensSection(page);
    const before = await apiTokens(page);

    await page.locator(TOKEN_LABEL_INPUT).fill('');
    await page.locator(CREATE_BTN).click();
    await expect(page.locator('.settings-list-error')).toBeVisible();

    // A refused request issued nothing.
    expect(await apiTokens(page)).toHaveLength(before.length);
  });
});
