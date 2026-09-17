import { test, expect, type Page } from '@playwright/test';

// API tokens manager in Settings → About (GET/POST /api/tokens, DELETE /api/tokens/{id}) and the
// bearer credential those routes hand out.
//
// The section issues a token whose plaintext is shown exactly once, lists the rows by label, and
// revokes on click. The spec also proves the point of the whole feature from the browser's side:
// the issued token answers the task and project routes it is scoped to with no cookie at all, and
// answers 403 on one that is not on its list.
//
// Runs in the `isolated` project (settings UI + it changes server token state), and revokes every
// token it creates so the lane server is left with none.

const SETTINGS_BTN = '.icon-btn[aria-label="Settings"]';
const ABOUT_TAB = 'button.tab-btn:has-text("About")';
const TOKENS_SECTION = '.tokens-section';
const TOKEN_ROW = '.tokens-section .token-row';
const TOKEN_LABEL_INPUT = '.tokens-section input[type="text"]';
const CREATE_BTN = 'button[aria-label="create-api-token"]';
const REFRESH_BTN = 'button[aria-label="refresh-tokens"]';
const SECRET = '.tokens-secret';
const ROW_REVOKE = 'button[aria-label^="revoke-token-"]';
const CONFIRM_BTN = '.btn-confirm';

/** Open Settings, switch to the About tab and scroll the API tokens section into view. */
async function openTokensSection(page: Page) {
  await page.goto('/');
  await page.waitForSelector('.add-task-btn, .icon-btn[aria-label="Settings"]', { timeout: 10000 });
  await page.locator(SETTINGS_BTN).first().click();
  await page.locator(ABOUT_TAB).click();
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

/** Present a token with no cookie: what a headless client does. */
async function callWithToken(page: Page, method: string, path: string, token: string): Promise<number> {
  return await page.evaluate(async ({ method, path, token }) => {
    const res = await fetch(path, {
      method,
      headers: { Authorization: `Bearer ${token}` },
      credentials: 'omit',
    });
    return res.status;
  }, { method, path, token });
}

/** Revoke through the UI and wait for the row to go. */
async function revokeToken(page: Page, id: string) {
  await page.locator(TOKENS_SECTION).scrollIntoViewIfNeeded();
  await page.locator(`button[aria-label="revoke-token-${id}"]`).click();
  await page.locator(CONFIRM_BTN).click();
  await expect(page.locator(`${TOKEN_ROW}[data-token-id="${id}"]`)).toHaveCount(0, { timeout: 10000 });
}

/** The server's own list — the row ids are what the UI's revoke labels must carry. */
async function apiTokens(page: Page): Promise<Array<{ id: string; label: string; last_used_at: string }>> {
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
    await page.locator(SETTINGS_BTN).first().click();
    await page.locator(ABOUT_TAB).click();
    await page.locator(TOKENS_SECTION).scrollIntoViewIfNeeded();
    await expect(page.locator(TOKEN_ROW, { hasText: 'playwright importer' })).toBeVisible();
    await expect(page.locator(SECRET)).toHaveCount(0);
    await expect(page.locator(TOKENS_SECTION)).not.toContainText(secret);

    await revokeToken(page, row!.id);
    expect((await apiTokens(page)).find(r => r.id === row!.id)).toBeUndefined();
  });

  test('the token authenticates its own routes and nothing else', async ({ page }) => {
    await openTokensSection(page);
    const secret = await createToken(page, 'playwright scope probe');
    const id = (await apiTokens(page)).find(r => r.label === 'playwright scope probe')!.id;

    await expect.poll(() => callWithToken(page, 'GET', '/api/projects', secret)).toBe(200);
    await expect.poll(() => callWithToken(page, 'GET', '/api/config', secret)).toBe(403);
    await expect.poll(() => callWithToken(page, 'GET', '/api/tokens', secret)).toBe(403);

    // Using it is what the row reports as "last used".
    await expect.poll(async () => (await apiTokens(page)).find(r => r.id === id)?.last_used_at, {
      message: 'a request authenticated by the token stamps last_used_at',
    }).not.toBe('');

    await revokeToken(page, id);
    await page.locator(REFRESH_BTN).click();
    await expect(page.locator(`${TOKEN_ROW}[data-token-id="${id}"]`)).toHaveCount(0);

    // Revocation is immediate: the next request presenting it authenticates as nothing.
    await expect.poll(() => callWithToken(page, 'GET', '/api/projects', secret)).toBe(401);
  });

  test('a label is required', async ({ page }) => {
    await openTokensSection(page);
    const before = await apiTokens(page);

    await page.locator(TOKEN_LABEL_INPUT).fill('');
    await page.locator(CREATE_BTN).click();
    await expect(page.locator('.tokens-error')).toBeVisible();

    // A refused request issued nothing.
    expect(await apiTokens(page)).toHaveLength(before.length);
  });
});
